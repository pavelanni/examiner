package handler

import (
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

// draftsToImports returns the decoded questions for prefilling the constructor.
func draftsToImports(drafts []questionimport.Draft) []model.QuestionImport {
	out := make([]model.QuestionImport, 0, len(drafts))
	for _, d := range drafts {
		out = append(out, d.QuestionImport)
	}
	return out
}

func (h *Handler) renderTeacherCreateTest(w http.ResponseWriter, r *http.Request, status int, questions []model.QuestionImport, filename, customName string, published bool, errs []string) {
	user := model.UserFromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	component := views.TeacherCreateTestPage(user.DisplayName, model.CSRFTokenFromContext(r.Context()), questions, filename, customName, published, errs)
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

	files, err := h.listOwnedQuestionFiles(user)
	if err != nil {
		slog.Error("failed to check question file owner", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	filename := r.URL.Query().Get("file")
	var existingQuestions []model.QuestionImport

	if filename != "" {
		baseName := filepath.Base(filename)
		if !h.checkQuestionFileOwner(w, user, baseName) {
			return
		}
		filePath := filepath.Join("questions", baseName)

		fileBytes, err := os.ReadFile(filePath)
		if err == nil {
			// Prefill whatever can be read; saving validates again.
			drafts, _ := questionimport.Decode(fileBytes)
			existingQuestions = draftsToImports(drafts)
		} else {
			slog.Warn("failed to read file for editing", "file", filePath, "error", err)
		}
	}

	// A new file is published by default; an existing one keeps the state
	// of the teacher's questions in it, so saving does not republish it.
	published := true
	if len(existingQuestions) > 0 {
		keys := make([]store.QuestionKey, 0, len(existingQuestions))
		for _, q := range existingQuestions {
			keys = append(keys, store.QuestionKey{Topic: q.Topic, TextNorm: model.NormalizeQuestionText(q.Text)})
		}
		var err error
		if published, err = h.store.AllPublished(user.ID, keys); err != nil {
			slog.Error("failed to read publication state", "file", filename, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	h.renderTeacherCreateTest(w, r, http.StatusOK, existingQuestions, filename, "", published, nil)
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

// handleTeacherUpload processes question file uploads and test definitions submitted by teachers.
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
	file, _, err := r.FormFile("questions_file")
	if err == nil {
		defer func() { _ = file.Close() }()
		b, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "failed to read file", http.StatusInternalServerError)
			return
		}
		data = b
	} else {
		txt := r.FormValue("questions_json")
		if txt == "" {
			http.Error(w, "no input provided", http.StatusBadRequest)
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
	if len(errs) > 0 {
		h.renderTeacherCreateTest(w, r, http.StatusBadRequest, draftsToImports(drafts), editingFile, customFilename, published,
			questionimport.Localize(r.Context(), errs))
		return
	}

	var safeName string
	var oldDrafts []questionimport.Draft

	if editingFile != "" {
		if !h.checkQuestionFileOwner(w, user, editingFile) {
			return
		}
		oldPath = filepath.Join("questions", editingFile)
		if oldBytes, err := os.ReadFile(oldPath); err == nil {
			oldDrafts, _ = questionimport.Decode(oldBytes)
		}
		if customFilename != "" {
			base := filepath.Base(customFilename)
			base = strings.TrimSuffix(base, ".json")
			safeName = fmt.Sprintf("teacher_%s_%s.json", user.Username, base)
		} else {
			safeName = editingFile
		}
	} else {
		if customFilename != "" {
			base := filepath.Base(customFilename)
			base = strings.TrimSuffix(base, ".json")
			safeName = fmt.Sprintf("teacher_%s_%s.json", user.Username, base)
		} else {
			safeName = fmt.Sprintf("teacher_%s_%d.json", user.Username, time.Now().Unix())
		}
	}
	// Reject a name the file system would refuse before anything is
	// written.
	if !validQuestionFileName(safeName) {
		h.renderTeacherCreateTest(w, r, http.StatusBadRequest, draftsToImports(drafts), editingFile, customFilename, published,
			[]string{appI18n.T(r.Context(), "QImportErrFileName")})
		return
	}
	// The new name must stay in the user's own namespace: with users ivan
	// and ivan_mid, ivan saving "mid_exam" would create ivan_mid's file.
	if ok, err := h.ownsQuestionFile(user, safeName); err != nil {
		slog.Error("failed to check question file owner", "file", safeName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !ok {
		h.renderTeacherCreateTest(w, r, http.StatusBadRequest, draftsToImports(drafts), editingFile, customFilename, published,
			[]string{appI18n.T(r.Context(), "QImportErrFileNameTaken")})
		return
	}
	outPath := filepath.Join("questions", safeName)

	// The file is published (and a renamed file retired) as the last step
	// of the transaction. A failed publish rolls the transaction back; a
	// failed commit makes saveTeacherQuestionFile restore the files.
	var res questionimport.Result
	err = saveTeacherQuestionFile(oldPath, outPath, func(publish func([]byte) error) error {
		return h.store.InQuestionTx(r.Context(), func(tx *store.QuestionTx) error {
			var err error
			res, err = questionimport.Apply(tx, drafts, questionimport.Options{
				Mode: questionimport.ModeUpdateExisting,
				// TODO: derive course ID from context/config when multi-course support lands.
				CourseID:  1,
				Old:       oldDrafts,
				Actor:     user,
				Published: published,
			})
			if err != nil {
				return err
			}
			// The file holds only the saved questions: a duplicate within
			// the file is left out.
			written, err := savedQuestionsFile(data, drafts, res.Saved)
			if err != nil {
				return err
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
	switch {
	case err == nil:
	case errors.As(err, &conflict):
		h.renderTeacherCreateTest(w, r, http.StatusBadRequest, draftsToImports(drafts), editingFile, customFilename, published,
			questionimport.Localize(r.Context(), conflict.Errs))
		return
	default:
		slog.Error("failed to save questions", "user", user.Username, "file", outPath, "error", err)
		h.renderTeacherCreateTest(w, r, http.StatusInternalServerError, draftsToImports(drafts), editingFile, customFilename, published,
			[]string{appI18n.T(r.Context(), "QImportErrInternal")})
		return
	}

	warnings := questionimport.LocalizeWarnings(r.Context(), res.Warnings)
	slog.Info("teacher uploaded questions", "user", user.Username, "file", outPath,
		"inserted", res.Inserted, "updated", res.Updated, "skipped", res.Skipped)
	if len(warnings) == 0 {
		http.Redirect(w, r, h.path("/teacher/profile"), http.StatusSeeOther)
		return
	}
	msg := appI18n.Td(r.Context(), "QImportSummary", map[string]any{
		"Inserted": res.Inserted, "Updated": res.Updated, "Skipped": res.Skipped,
	}) + " " + strings.Join(warnings, " ")
	files, err := h.listOwnedQuestionFiles(user)
	if err != nil {
		// The save succeeded; show the message without the file list.
		slog.Error("failed to check question file owner", "error", err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.TeacherProfilePage(user.DisplayName, files, msg).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
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
