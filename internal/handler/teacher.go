package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/questionimport"
	"github.com/pavelanni/examiner/internal/store"
)

func teacherQuestionFilePrefix(username string) string {
	return fmt.Sprintf("teacher_%s_", username)
}

func teacherOwnsQuestionFile(username, filename string) bool {
	return strings.HasPrefix(filepath.Base(filename), teacherQuestionFilePrefix(username))
}

func listTeacherFiles(username string) []string {
	files := []string{}
	entries, err := os.ReadDir("questions")
	if err != nil {
		return files
	}
	prefix := teacherQuestionFilePrefix(username)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) == ".json" && strings.HasPrefix(name, prefix) {
			files = append(files, name)
		}
	}
	return files
}

// draftsToImports returns the decoded questions for prefilling the constructor.
func draftsToImports(drafts []questionimport.Draft) []model.QuestionImport {
	out := make([]model.QuestionImport, 0, len(drafts))
	for _, d := range drafts {
		out = append(out, d.QuestionImport)
	}
	return out
}

func (h *Handler) renderTeacherCreateTest(w http.ResponseWriter, r *http.Request, status int, questions []model.QuestionImport, filename, customName string, errs []string) {
	user := model.UserFromContext(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	component := views.TeacherCreateTestPage(user.DisplayName, model.CSRFTokenFromContext(r.Context()), questions, filename, customName, errs)
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

	files := listTeacherFiles(user.Username)

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
		if !teacherOwnsQuestionFile(user.Username, baseName) {
			http.Error(w, "forbidden", http.StatusForbidden)
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

	h.renderTeacherCreateTest(w, r, http.StatusOK, existingQuestions, filename, "", nil)
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

	// Validate the whole payload before touching the disk or the DB.
	drafts, errs := questionimport.Parse(data)
	if len(errs) > 0 {
		h.renderTeacherCreateTest(w, r, http.StatusBadRequest, draftsToImports(drafts), editingFile, customFilename,
			questionimport.Localize(r.Context(), errs))
		return
	}

	var safeName string
	var oldTexts []string

	if editingFile != "" {
		if !teacherOwnsQuestionFile(user.Username, editingFile) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		oldPath = filepath.Join("questions", editingFile)
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
	outPath := filepath.Join("questions", safeName)

	internalError := func(msg string, err error) {
		slog.Error(msg, "user", user.Username, "file", outPath, "error", err)
		h.renderTeacherCreateTest(w, r, http.StatusInternalServerError, draftsToImports(drafts), editingFile, customFilename,
			[]string{appI18n.T(r.Context(), "QImportErrInternal")})
	}

	// The file is published (and a renamed file retired) as the last step
	// of the transaction. A failed publish rolls the transaction back; a
	// failed commit makes saveTeacherQuestionFile restore the files.
	sum := sha256.Sum256(data)
	var res questionimport.Result
	err = saveTeacherQuestionFile(oldPath, outPath, func(publish func([]byte) error) error {
		return h.store.InQuestionTx(r.Context(), func(tx *store.QuestionTx) error {
			var err error
			res, err = questionimport.Apply(tx, drafts, questionimport.Options{
				Mode: questionimport.ModeUpdateExisting,
				// TODO: derive course ID from context/config when multi-course support lands.
				CourseID: 1,
				OldTexts: oldTexts,
				File:     &questionimport.FileRecord{Name: safeName, Hash: hex.EncodeToString(sum[:])},
			})
			if err != nil {
				return err
			}
			return publish(data)
		})
	})
	if err != nil {
		internalError("failed to save questions", err)
		return
	}

	for _, msg := range questionimport.LocalizeWarnings(r.Context(), res.Warnings) {
		slog.Warn("teacher question skipped", "user", user.Username, "file", outPath, "reason", msg)
	}
	slog.Info("teacher uploaded questions", "user", user.Username, "file", outPath,
		"inserted", res.Inserted, "updated", res.Updated, "skipped", res.Skipped)

	http.Redirect(w, r, h.path("/teacher/profile"), http.StatusSeeOther)
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
	if !teacherOwnsQuestionFile(user.Username, name) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	path := filepath.Join("questions", name)
	http.ServeFile(w, r, path)
}
