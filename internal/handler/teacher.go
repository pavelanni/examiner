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
	"slices"
	"strconv"
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
func (h *Handler) checkQuestionFileOwner(w http.ResponseWriter, r *http.Request, user *model.User, name string) bool {
	ok, err := h.ownsQuestionFile(user, name)
	if err != nil {
		slog.Error("failed to check question file owner", "file", name, "error", err)
		httpError(w, r, http.StatusInternalServerError, "ErrInternal")
		return false
	}
	if !ok {
		httpError(w, r, http.StatusForbidden, "ErrForbidden")
		return false
	}
	return true
}

// listOwnedQuestionFiles returns the question files user owns.
func (h *Handler) listOwnedQuestionFiles(user *model.User) ([]string, error) {
	files := []string{}
	entries, err := os.ReadDir("questions")
	if err != nil {
		return files, nil
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		ok, err := h.ownsQuestionFile(user, e.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			files = append(files, e.Name())
		}
	}
	return files, nil
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
// stands for: the active question with its topic and normalized text, if
// user may edit it. Question files hold no IDs, so this maps a file to the
// database when it is opened or replaced; edits are then saved by ID. A
// draft without such a question, or whose question an earlier draft
// already took, gets 0.
func (h *Handler) questionIDs(user *model.User, drafts []questionimport.Draft) ([]int64, error) {
	ids := make([]int64, len(drafts))
	taken := make(map[int64]bool, len(drafts))
	for i, d := range drafts {
		if d.Text == "" {
			continue
		}
		// TODO: derive course ID from context/config when multi-course support lands.
		q, err := h.store.FindQuestionByTopicAndTextNorm(1, d.Topic, model.NormalizeQuestionText(d.Text))
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

// duplicateDecisions reads the dup_<N> choices of the duplicate form:
// "skip", or "update:<ID>" naming the stored question the update was
// offered for.
func duplicateDecisions(r *http.Request, drafts []questionimport.Draft) map[int]questionimport.Choice {
	out := make(map[int]questionimport.Choice)
	for _, d := range drafts {
		v := r.FormValue(fmt.Sprintf("dup_%d", d.Number))
		if v == "skip" {
			out[d.Number] = questionimport.Choice{Decision: questionimport.DecisionSkip}
			continue
		}
		if id, ok := strings.CutPrefix(v, "update:"); ok {
			if target, err := strconv.ParseInt(id, 10, 64); err == nil {
				out[d.Number] = questionimport.Choice{Decision: questionimport.DecisionUpdate, Target: target}
			}
		}
	}
	return out
}

// keptDecisions returns the decisions of this save for the questions that
// are not asked about again, so the next duplicate form sends them too.
// Apply checks them again on that save: an update whose target no longer
// matches is asked about again.
func keptDecisions(decisions map[int]questionimport.Choice, pending []questionimport.Duplicate) []views.KeptDecision {
	asked := make(map[int]bool, len(pending))
	for _, d := range pending {
		asked[d.Question] = true
	}
	out := make([]views.KeptDecision, 0, len(decisions))
	for n, c := range decisions {
		if asked[n] {
			continue
		}
		switch c.Decision {
		case questionimport.DecisionSkip:
			out = append(out, views.KeptDecision{N: n, Value: "skip"})
		case questionimport.DecisionUpdate:
			out = append(out, views.KeptDecision{N: n, Value: fmt.Sprintf("update:%d", c.Target)})
		}
	}
	slices.SortFunc(out, func(a, b views.KeptDecision) int { return a.N - b.N })
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
		httpError(w, r, http.StatusUnauthorized, "ErrUnauthorized")
		return
	}

	files, err := h.listOwnedQuestionFiles(user)
	if err != nil {
		slog.Error("failed to check question file owner", "error", err)
		httpError(w, r, http.StatusInternalServerError, "ErrInternal")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.TeacherProfilePage(user.DisplayName, files, "").Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleTeacherCreateTest(w http.ResponseWriter, r *http.Request) {
	user := model.UserFromContext(r.Context())
	if user == nil {
		httpError(w, r, http.StatusUnauthorized, "ErrUnauthorized")
		return
	}

	filename := r.URL.Query().Get("file")
	var drafts []questionimport.Draft

	if filename != "" {
		baseName := filepath.Base(filename)
		if !h.checkQuestionFileOwner(w, r, user, baseName) {
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
				httpError(w, r, http.StatusInternalServerError, "ErrInternal")
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
			httpError(w, r, http.StatusInternalServerError, "ErrInternal")
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
		httpError(w, r, http.StatusUnauthorized, "ErrUnauthorized")
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

// handleTeacherUpload processes question file uploads and test definitions submitted by teachers.
func (h *Handler) handleTeacherUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		httpError(w, r, http.StatusBadRequest, "ErrFileTooLarge")
		return
	}

	user := model.UserFromContext(r.Context())
	if user == nil {
		httpError(w, r, http.StatusUnauthorized, "ErrUnauthorized")
		return
	}

	var data []byte
	fromFile := false
	file, _, err := r.FormFile("questions_file")
	if err == nil {
		defer func() { _ = file.Close() }()
		b, err := io.ReadAll(file)
		if err != nil {
			httpError(w, r, http.StatusInternalServerError, "ErrFileRead")
			return
		}
		data = b
		fromFile = true
	} else {
		txt := r.FormValue("questions_json")
		if txt == "" {
			httpError(w, r, http.StatusBadRequest, "ErrNoInput")
			return
		}
		data = []byte(txt)
	}

	editingFile := r.FormValue("editing_file")
	if editingFile != "" {
		editingFile = filepath.Base(editingFile)
	}
	h.teacherUploadMu.Lock()
	defer h.teacherUploadMu.Unlock()
	var oldPath string
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
		if !h.checkQuestionFileOwner(w, r, user, editingFile) {
			return
		}
		oldPath = filepath.Join("questions", editingFile)
		if oldBytes, err := os.ReadFile(oldPath); err == nil {
			oldDrafts, _ := questionimport.Decode(oldBytes)
			ids, err := h.questionIDs(user, oldDrafts)
			if err != nil {
				slog.Error("failed to find stored questions", "file", oldPath, "error", err)
				httpError(w, r, http.StatusInternalServerError, "ErrInternal")
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
		safeName = editingFile
	} else {
		safeName = fmt.Sprintf("teacher_%s_%d.json", user.Username, time.Now().Unix())
	}
	// Reject a name the file system would refuse before anything is
	// written.
	if !validQuestionFileName(safeName) {
		render(http.StatusBadRequest, []string{appI18n.T(r.Context(), "QImportErrFileName")}, nil, "")
		return
	}
	// The new name must stay in the user's own namespace: with users ivan
	// and ivan_mid, ivan saving "mid_exam" would create ivan_mid's file.
	if ok, err := h.ownsQuestionFile(user, safeName); err != nil {
		slog.Error("failed to check question file owner", "file", safeName, "error", err)
		httpError(w, r, http.StatusInternalServerError, "ErrInternal")
		return
	} else if !ok {
		render(http.StatusBadRequest, []string{appI18n.T(r.Context(), "QImportErrFileNameTaken")}, nil, "")
		return
	}
	outPath := filepath.Join("questions", safeName)

	// Decisions about duplicates count only for the questions they were
	// asked about; an edited test is checked again.
	dupToken := duplicateToken(drafts)
	var decisions map[int]questionimport.Choice
	if r.FormValue("dup_token") == dupToken {
		decisions = duplicateDecisions(r, drafts)
	}

	// The file is published (and a renamed file retired) as the last step
	// of the transaction. A failed publish rolls the transaction back; a
	// failed commit makes saveTeacherQuestionFile restore the files.
	var res questionimport.Result
	err = saveTeacherQuestionFile(oldPath, outPath, func(publish func([]byte) error) error {
		return h.store.InQuestionTx(r.Context(), func(tx *store.QuestionTx) error {
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
			if len(res.Saved) == 0 {
				return errAllSkipped
			}
			// The file holds only the saved questions, so it matches the
			// database: skipped duplicates are left out.
			written, err := savedQuestionsFile(data, drafts, res.Saved)
			if err != nil {
				return fmt.Errorf("encode questions file: %w", err)
			}
			if editingFile != "" && editingFile != safeName {
				if err := tx.DeleteImportedFileHash(editingFile); err != nil {
					return err
				}
			}
			sum := sha256.Sum256(written)
			if err := tx.SetImportedFileHash(safeName, hex.EncodeToString(sum[:])); err != nil {
				return err
			}
			return publish(written)
		})
	})
	var conflict *questionimport.ConflictError
	var dups *questionimport.DuplicatesError
	switch {
	case err == nil:
	case errors.As(err, &conflict):
		render(http.StatusBadRequest, questionimport.Localize(r.Context(), conflict.Errs), nil, "")
		return
	case errors.As(err, &dups):
		h.renderTeacherCreateTest(w, r, http.StatusConflict, views.TeacherBuilder{
			Questions: builderQuestions(drafts), Filename: editingFile, CustomName: customFilename,
			Published: published, Dups: dupChoices(r.Context(), dups.Dups), DupToken: dupToken,
			Kept: keptDecisions(decisions, dups.Dups),
		})
		return
	case errors.Is(err, errAllSkipped):
		render(http.StatusBadRequest, []string{appI18n.T(r.Context(), "QImportErrAllSkipped")}, nil, "")
		return
	default:
		slog.Error("failed to save questions", "user", user.Username, "file", outPath, "error", err)
		render(http.StatusInternalServerError, []string{appI18n.T(r.Context(), "QImportErrInternal")}, nil, "")
		return
	}

	for _, msg := range questionimport.LocalizeWarnings(r.Context(), res.Warnings) {
		slog.Info("teacher question skipped", "user", user.Username, "file", outPath, "reason", msg)
	}
	slog.Info("teacher uploaded questions", "user", user.Username, "file", outPath,
		"inserted", res.Inserted, "updated", res.Updated, "skipped", res.Skipped)
	http.Redirect(w, r, h.path("/teacher/profile"), http.StatusSeeOther)
}

// savedQuestionsFile returns the file content to publish: data itself when
// every question was saved, otherwise only the saved ones.
func savedQuestionsFile(data []byte, drafts []questionimport.Draft, saved []int) ([]byte, error) {
	if len(saved) == len(drafts) {
		return data, nil
	}
	byNumber := make(map[int]model.QuestionImport, len(drafts))
	for _, d := range drafts {
		byNumber[d.Number] = d.QuestionImport
	}
	out := make([]model.QuestionImport, 0, len(saved))
	for _, n := range saved {
		out = append(out, byNumber[n])
	}
	return json.MarshalIndent(out, "", "  ")
}

func (h *Handler) handleTeacherDownload(w http.ResponseWriter, r *http.Request) {
	user := model.UserFromContext(r.Context())
	if user == nil {
		httpError(w, r, http.StatusUnauthorized, "ErrUnauthorized")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		httpError(w, r, http.StatusBadRequest, "ErrMissingName")
		return
	}
	if filepath.Base(name) != name {
		httpError(w, r, http.StatusBadRequest, "ErrInvalidFilename")
		return
	}
	if !h.checkQuestionFileOwner(w, r, user, name) {
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
