package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/access"
	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/questionimport"
	"github.com/pavelanni/examiner/internal/store"
)

// errAllSkipped rolls back a constructor save in which every question was
// skipped as a duplicate.
var errAllSkipped = errors.New("every question was skipped")

// ownsQuestionFile reports whether user owns the question file name under
// the shared longest-prefix rule (access.QuestionFileOwner).
func (h *Handler) ownsQuestionFile(user *model.User, name string) (bool, error) {
	users, err := h.store.ListUsers()
	if err != nil {
		return false, err
	}
	owner := access.QuestionFileOwner(users, filepath.Base(name))
	return owner != nil && owner.ID == user.ID, nil
}

// checkQuestionFileOwner writes 403 (or 500) and returns false unless user
// owns the question file name.
func (h *Handler) checkQuestionFileOwner(w http.ResponseWriter, user *model.User, name string) bool {
	ok, err := h.ownsQuestionFile(user, name)
	if err != nil {
		slog.Error("failed to check question file owner", "file", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// builderQuestions returns the decoded questions, with the stored questions
// they edit, for prefilling the constructor.
func builderQuestions(drafts []questionimport.Draft) []views.BuilderQuestion {
	out := make([]views.BuilderQuestion, 0, len(drafts))
	for _, d := range drafts {
		out = append(out, views.BuilderQuestion{ID: d.ID, QuestionImport: d.QuestionImport})
	}
	return out
}

// questionIDs finds the stored question each draft of a question file
// stands for: the one with exactly its text, if user may edit it. Question
// files hold no IDs, so this maps a file to the database when it is opened
// or replaced; edits are then saved by ID. A draft without such a question,
// or whose question an earlier draft already took, gets 0.
func (h *Handler) questionIDs(user *model.User, drafts []questionimport.Draft) ([]int64, error) {
	ids := make([]int64, len(drafts))
	taken := make(map[int64]bool, len(drafts))
	for i, d := range drafts {
		if d.Text == "" {
			continue
		}
		// TODO: derive course ID from context/config when multi-course support lands.
		q, err := h.store.FindQuestionByCourseAndText(1, d.Text)
		if err != nil {
			return nil, err
		}
		if q != nil && !taken[q.ID] && access.CanEditQuestion(user, *q) {
			ids[i] = q.ID
			taken[q.ID] = true
		}
	}
	return ids, nil
}

// assignIDs sets the stored question each draft edits from the
// constructor's question_ids field (a JSON array parallel to the drafts).
// Only questions of the file being edited may be named, each once; any
// other ID leaves its draft a new question.
func assignIDs(drafts []questionimport.Draft, field string, allowed []int64) {
	if field == "" || len(allowed) == 0 {
		return
	}
	var ids []int64
	if err := json.Unmarshal([]byte(field), &ids); err != nil || len(ids) != len(drafts) {
		slog.Warn("ignoring malformed question_ids", "count", len(ids), "questions", len(drafts), "error", err)
		return
	}
	free := make(map[int64]bool, len(allowed))
	for _, id := range allowed {
		free[id] = true
	}
	for i, id := range ids {
		if free[id] {
			drafts[i].ID = id
			delete(free, id)
		}
	}
}

// duplicateToken identifies a version of the submitted questions, so
// duplicate decisions made for one version are not applied to another.
func duplicateToken(drafts []questionimport.Draft) string {
	b, err := json.Marshal(builderQuestions(drafts))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// duplicateDecisions reads the dup_<N> choices of the duplicate form.
func duplicateDecisions(r *http.Request, drafts []questionimport.Draft) map[int]questionimport.Decision {
	out := make(map[int]questionimport.Decision)
	for _, d := range drafts {
		switch r.FormValue(fmt.Sprintf("dup_%d", d.Number)) {
		case "skip":
			out[d.Number] = questionimport.DecisionSkip
		case "update":
			out[d.Number] = questionimport.DecisionUpdate
		}
	}
	return out
}

// dupChoices renders the duplicates that need a decision.
func dupChoices(ctx context.Context, dups []questionimport.Duplicate) []views.DupChoice {
	out := make([]views.DupChoice, 0, len(dups))
	for _, d := range dups {
		id, _ := d.Params["ID"].(int64)
		out = append(out, views.DupChoice{N: d.Question, Msg: questionimport.LocalizeDuplicate(ctx, d), CanUpdate: d.CanUpdate, UpdateID: id})
	}
	return out
}

// savedQuestionsFile returns the question file to write after a save: the
// submitted data or, when questions were skipped, only the saved ones, so
// the file matches the database.
func savedQuestionsFile(data []byte, drafts []questionimport.Draft, res questionimport.Result) ([]byte, error) {
	if res.Skipped == 0 {
		return data, nil
	}
	skipped := make(map[int]bool, len(res.Warnings))
	for _, w := range res.Warnings {
		skipped[w.Question] = true
	}
	kept := make([]model.QuestionImport, 0, len(drafts))
	for _, d := range drafts {
		if !skipped[d.Number] {
			kept = append(kept, d.QuestionImport)
		}
	}
	return json.MarshalIndent(kept, "", "  ")
}

func (h *Handler) renderTeacherCreateTest(w http.ResponseWriter, r *http.Request, status int, b views.TeacherBuilder) {
	user := model.UserFromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	component := views.TeacherCreateTestPage(user.DisplayName, model.CSRFTokenFromContext(r.Context()), b)
	if err := component.Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleTeacherProfile(w http.ResponseWriter, r *http.Request) {
	user := model.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	files := []string{}
	entries, err := os.ReadDir("questions")
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			ok, err := h.ownsQuestionFile(user, e.Name())
			if err != nil {
				slog.Error("failed to check question file owner", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if ok {
				files = append(files, e.Name())
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.TeacherProfilePage(user.DisplayName, files).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleTeacherCreateTest(w http.ResponseWriter, r *http.Request) {
	user := model.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	filename := r.URL.Query().Get("file")
	var drafts []questionimport.Draft

	if filename != "" {
		baseName := filepath.Base(filename)
		if !h.checkQuestionFileOwner(w, user, baseName) {
			return
		}
		filePath := filepath.Join("questions", baseName)

		fileBytes, err := os.ReadFile(filePath)
		if err == nil {
			// Prefill whatever can be read; saving validates again.
			drafts, _ = questionimport.Decode(fileBytes)
			ids, err := h.questionIDs(user, drafts)
			if err != nil {
				slog.Error("failed to find stored questions", "file", filename, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for i := range drafts {
				drafts[i].ID = ids[i]
			}
		} else {
			slog.Warn("failed to read file for editing", "file", filePath, "error", err)
		}
	}

	// A new file is published by default; an existing one keeps the state
	// of the teacher's questions in it, so saving does not republish it.
	published := true
	if len(drafts) > 0 {
		keys := make([]store.QuestionKey, 0, len(drafts))
		for _, d := range drafts {
			keys = append(keys, store.QuestionKey{Topic: d.Topic, TextNorm: model.NormalizeQuestionText(d.Text)})
		}
		var err error
		if published, err = h.store.AllPublished(user.ID, keys); err != nil {
			slog.Error("failed to read publication state", "file", filename, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	h.renderTeacherCreateTest(w, r, http.StatusOK, views.TeacherBuilder{
		Questions: builderQuestions(drafts), Filename: filename, Published: published,
	})
}

func (h *Handler) handleTeacherMe(w http.ResponseWriter, r *http.Request) {
	user := model.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":           user.ID,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"role":         user.Role,
		"active":       user.Active,
	})
}

func (h *Handler) handleTeacherUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "file too large", http.StatusBadRequest)
		return
	}

	user := model.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var data []byte
	fromFile := false
	file, _, err := r.FormFile("questions_file")
	if err == nil {
		defer func() { _ = file.Close() }()
		b, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "failed to read file", http.StatusInternalServerError)
			return
		}
		data = b
		fromFile = true
	} else {
		txt := r.FormValue("questions_json")
		if txt == "" {
			http.Error(w, "no input provided", http.StatusBadRequest)
			return
		}
		data = []byte(txt)
	}

	editingFile := r.FormValue("editing_file")
	customFilename := strings.TrimSpace(r.FormValue("custom_filename"))
	published := r.FormValue("publish") == "1"

	// Validate the whole payload before touching the disk or the DB.
	drafts, errs := questionimport.Parse(data)
	render := func(status int, errs []string, dups []views.DupChoice, dupToken string) {
		h.renderTeacherCreateTest(w, r, status, views.TeacherBuilder{
			Questions: builderQuestions(drafts), Filename: editingFile, CustomName: customFilename,
			Published: published, Errs: errs, Dups: dups, DupToken: dupToken,
		})
	}

	// The stored questions of the edited file: blocks of the constructor
	// may name them to be updated by ID, and those dropped are deleted.
	var oldIDs []int64
	if editingFile != "" {
		if !h.checkQuestionFileOwner(w, user, editingFile) {
			return
		}
		oldPath := filepath.Join("questions", filepath.Base(editingFile))
		if oldBytes, err := os.ReadFile(oldPath); err == nil {
			oldDrafts, _ := questionimport.Decode(oldBytes)
			ids, err := h.questionIDs(user, oldDrafts)
			if err != nil {
				slog.Error("failed to find stored questions", "file", oldPath, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for _, id := range ids {
				if id != 0 {
					oldIDs = append(oldIDs, id)
				}
			}
		}
	}
	// An uploaded file replaces the blocks, so their IDs do not apply.
	if !fromFile {
		assignIDs(drafts, r.FormValue("question_ids"), oldIDs)
	}

	if len(errs) > 0 {
		render(http.StatusBadRequest, questionimport.Localize(r.Context(), errs), nil, "")
		return
	}

	var safeName string
	if customFilename != "" {
		base := filepath.Base(customFilename)
		base = strings.TrimSuffix(base, ".json")
		safeName = fmt.Sprintf("teacher_%s_%s.json", user.Username, base)
	} else if editingFile != "" {
		safeName = filepath.Base(editingFile)
	} else {
		safeName = fmt.Sprintf("teacher_%s_%d.json", user.Username, time.Now().Unix())
	}
	// Reject a name the file system would refuse before anything is
	// written: the rename happens only after the DB commit.
	if !validQuestionFileName(safeName) {
		render(http.StatusBadRequest, []string{appI18n.T(r.Context(), "QImportErrFileName")}, nil, "")
		return
	}
	// The new name must stay in the user's own namespace: with users ivan
	// and ivan_mid, ivan saving "mid_exam" would create ivan_mid's file.
	if ok, err := h.ownsQuestionFile(user, safeName); err != nil {
		slog.Error("failed to check question file owner", "file", safeName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !ok {
		render(http.StatusBadRequest, []string{appI18n.T(r.Context(), "QImportErrFileNameTaken")}, nil, "")
		return
	}
	outPath := filepath.Join("questions", safeName)

	fail := func(msgID, msg string, err error) {
		slog.Error(msg, "user", user.Username, "file", outPath, "error", err)
		render(http.StatusInternalServerError, []string{appI18n.T(r.Context(), msgID)}, nil, "")
	}

	// Decisions about duplicates count only for the questions they were
	// asked about; an edited test is checked again.
	dupToken := duplicateToken(drafts)
	var decisions map[int]questionimport.Decision
	if r.FormValue("dup_token") == dupToken {
		decisions = duplicateDecisions(r, drafts)
	}

	// Kept to restore the destination if the commit fails after the rename.
	prevData, prevErr := os.ReadFile(outPath)

	var res questionimport.Result
	var tmpPath string
	renamed := false
	err = h.store.InQuestionTx(r.Context(), func(tx *store.QuestionTx) error {
		var err error
		res, err = questionimport.Apply(tx, drafts, questionimport.Options{
			Mode: questionimport.ModeAsk,
			// TODO: derive course ID from context/config when multi-course support lands.
			CourseID:  1,
			OldIDs:    oldIDs,
			Decisions: decisions,
			Actor:     user,
			Published: published,
		})
		if err != nil {
			return err
		}
		if res.Inserted+res.Updated == 0 {
			return errAllSkipped
		}
		out, err := savedQuestionsFile(data, drafts, res)
		if err != nil {
			return fmt.Errorf("encode questions file: %w", err)
		}
		sum := sha256.Sum256(out)
		if err := tx.SetImportedFileHash(safeName, hex.EncodeToString(sum[:])); err != nil {
			return fmt.Errorf("record imported file: %w", err)
		}
		// Write the file next to its destination and rename it into place
		// as the last step: a failed rename rolls the transaction back, so
		// the DB and its imported_files hash never get ahead of the file.
		if tmpPath, err = writeTempQuestionsFile(out); err != nil {
			return fmt.Errorf("write temporary questions file: %w", err)
		}
		if err := os.Rename(tmpPath, outPath); err != nil {
			return fmt.Errorf("publish questions file: %w", err)
		}
		renamed = true
		return nil
	})
	if err != nil {
		if !renamed {
			if tmpPath != "" {
				if rmErr := os.Remove(tmpPath); rmErr != nil {
					slog.Warn("failed to remove temporary questions file", "path", tmpPath, "error", rmErr)
				}
			}
		} else if rsErr := restoreQuestionsFile(outPath, prevData, prevErr); rsErr != nil {
			// The commit failed after the file was replaced: the DB is
			// unchanged, the file is not.
			slog.Error("failed to restore questions file", "path", outPath, "error", rsErr)
			fail("QImportErrFileNotWritten", "failed to save questions", err)
			return
		}
		var conflict *questionimport.ConflictError
		var dups *questionimport.DuplicatesError
		switch {
		case errors.As(err, &conflict):
			render(http.StatusBadRequest, questionimport.Localize(r.Context(), conflict.Errs), nil, "")
		case errors.As(err, &dups):
			render(http.StatusConflict, nil, dupChoices(r.Context(), dups.Dups), dupToken)
		case errors.Is(err, errAllSkipped):
			render(http.StatusBadRequest, []string{appI18n.T(r.Context(), "QImportErrAllSkipped")}, nil, "")
		default:
			fail("QImportErrInternal", "failed to save questions", err)
		}
		return
	}

	// Remove the old file when the edit flow renames it.
	if editingFile != "" && safeName != filepath.Base(editingFile) {
		oldPath := filepath.Join("questions", filepath.Base(editingFile))
		if err := os.Remove(oldPath); err != nil {
			slog.Warn("failed to remove old question file", "path", oldPath, "error", err)
		}
	}

	for _, msg := range questionimport.LocalizeWarnings(r.Context(), res.Warnings) {
		slog.Info("teacher question skipped", "user", user.Username, "file", outPath, "reason", msg)
	}
	slog.Info("teacher uploaded questions", "user", user.Username, "file", outPath,
		"inserted", res.Inserted, "updated", res.Updated, "skipped", res.Skipped)

	http.Redirect(w, r, h.path("/teacher/profile"), http.StatusSeeOther)
}

// writeTempQuestionsFile writes data to a new temporary file in questions/
// and returns its path.
func writeTempQuestionsFile(data []byte) (string, error) {
	f, err := os.CreateTemp("questions", ".tmp-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// restoreQuestionsFile puts back the content path had before a save:
// prevData if it existed (prevErr == nil), otherwise no file at all.
func restoreQuestionsFile(path string, prevData []byte, prevErr error) error {
	if prevErr != nil {
		return os.Remove(path)
	}
	tmpPath, err := writeTempQuestionsFile(prevData)
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (h *Handler) handleTeacherDownload(w http.ResponseWriter, r *http.Request) {
	user := model.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	if filepath.Base(name) != name {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	if !h.checkQuestionFileOwner(w, user, name) {
		return
	}
	path := filepath.Join("questions", name)
	http.ServeFile(w, r, path)
}

// validQuestionFileName reports whether name can be created on both Linux
// and Windows: at most 255 bytes, no control characters, none of <>:"/\|?*.
func validQuestionFileName(name string) bool {
	if len(name) > 255 {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			return false
		}
	}
	return true
}
