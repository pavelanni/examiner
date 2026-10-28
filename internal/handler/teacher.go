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
	var oldTexts []string

	if editingFile != "" {
		if !h.checkQuestionFileOwner(w, user, editingFile) {
			return
		}
		oldPath := filepath.Join("questions", filepath.Base(editingFile))
		if oldBytes, err := os.ReadFile(oldPath); err == nil {
			oldDrafts, _ := questionimport.Decode(oldBytes)
			for _, d := range oldDrafts {
				oldTexts = append(oldTexts, d.Text)
			}
		}
		if customFilename != "" {
			base := filepath.Base(customFilename)
			base = strings.TrimSuffix(base, ".json")
			safeName = fmt.Sprintf("teacher_%s_%s.json", user.Username, base)
		} else {
			safeName = filepath.Base(editingFile)
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
	// written: the rename happens only after the DB commit.
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

	fail := func(msgID, msg string, err error) {
		slog.Error(msg, "user", user.Username, "file", outPath, "error", err)
		h.renderTeacherCreateTest(w, r, http.StatusInternalServerError, draftsToImports(drafts), editingFile, customFilename, published,
			[]string{appI18n.T(r.Context(), msgID)})
	}
	internalError := func(msg string, err error) { fail("QImportErrInternal", msg, err) }

	// Write the payload next to its destination first; it is renamed into
	// place only after the DB transaction commits.
	tmpPath, err := writeTempQuestionsFile(data)
	if err != nil {
		internalError("failed to write temporary questions file", err)
		return
	}

	sum := sha256.Sum256(data)
	var res questionimport.Result
	err = h.store.InQuestionTx(r.Context(), func(tx *store.QuestionTx) error {
		var err error
		res, err = questionimport.Apply(tx, drafts, questionimport.Options{
			Mode: questionimport.ModeUpdateExisting,
			// TODO: derive course ID from context/config when multi-course support lands.
			CourseID:  1,
			OldTexts:  oldTexts,
			File:      &questionimport.FileRecord{Name: safeName, Hash: hex.EncodeToString(sum[:])},
			Actor:     user,
			Published: published,
		})
		return err
	})
	if err != nil {
		if rmErr := os.Remove(tmpPath); rmErr != nil {
			slog.Warn("failed to remove temporary questions file", "path", tmpPath, "error", rmErr)
		}
		var conflict *questionimport.ConflictError
		if errors.As(err, &conflict) {
			h.renderTeacherCreateTest(w, r, http.StatusBadRequest, draftsToImports(drafts), editingFile, customFilename, published,
				questionimport.Localize(r.Context(), conflict.Errs))
			return
		}
		internalError("failed to save questions", err)
		return
	}

	// The DB is committed. A failed rename leaves the DB consistent;
	// saving again rewrites the file.
	if err := os.Rename(tmpPath, outPath); err != nil {
		if rmErr := os.Remove(tmpPath); rmErr != nil {
			slog.Warn("failed to remove temporary questions file", "path", tmpPath, "error", rmErr)
		}
		fail("QImportErrFileNotWritten", "failed to publish questions file", err)
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
		slog.Warn("teacher question skipped", "user", user.Username, "file", outPath, "reason", msg)
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
