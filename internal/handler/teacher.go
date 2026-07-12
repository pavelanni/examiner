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
	"github.com/pavelanni/examiner/internal/model"
)

func teacherQuestionFilePrefix(username string) string {
	return fmt.Sprintf("teacher_%s_", username)
}

func teacherOwnsQuestionFile(username, filename string) bool {
	return strings.HasPrefix(filepath.Base(filename), teacherQuestionFilePrefix(username))
}

func parseQuestionsFromJSON(data []byte) []model.QuestionImport {
	var questions []model.QuestionImport
	if err := json.Unmarshal(data, &questions); err == nil {
		return questions
	}
	var wrapper struct {
		Questions []model.QuestionImport `json:"questions"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil {
		return wrapper.Questions
	}
	return nil
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
		prefix := teacherQuestionFilePrefix(user.Username)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if filepath.Ext(name) == ".json" && strings.HasPrefix(name, prefix) {
				files = append(files, name)
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
		if !teacherOwnsQuestionFile(user.Username, baseName) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		filePath := filepath.Join("questions", baseName)

		fileBytes, err := os.ReadFile(filePath)
		if err == nil {
			existingQuestions = parseQuestionsFromJSON(fileBytes)
		} else {
			slog.Warn("failed to read file for editing", "file", filePath, "error", err)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	component := views.TeacherCreateTestPage(user.DisplayName, model.CSRFTokenFromContext(r.Context()), existingQuestions, filename)
	if err := component.Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
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

	// Validate the JSON structure against the compiled schema.
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.questionSchema.Validate(v); err != nil {
		http.Error(w, "schema validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	var questions []model.QuestionImport
	if err := json.Unmarshal(data, &questions); err != nil {
		var wrapper struct {
			TestName        string                 `json:"test_name"`
			TestDescription string                 `json:"test_description"`
			DefaultTopic    string                 `json:"default_topic"`
			Questions       []model.QuestionImport `json:"questions"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		questions = wrapper.Questions
	}
	if len(questions) == 0 {
		http.Error(w, "no questions found in JSON", http.StatusBadRequest)
		return
	}

	user := model.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	editingFile := r.FormValue("editing_file")
	customFilename := strings.TrimSpace(r.FormValue("custom_filename"))

	var safeName string
	var outPath string
	var oldQuestions []model.QuestionImport

	if editingFile != "" {
		if !teacherOwnsQuestionFile(user.Username, editingFile) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		oldPath := filepath.Join("questions", filepath.Base(editingFile))
		if oldBytes, err := os.ReadFile(oldPath); err == nil {
			oldQuestions = parseQuestionsFromJSON(oldBytes)
		}
		if customFilename != "" {
			base := filepath.Base(customFilename)
			base = strings.TrimSuffix(base, ".json")
			safeName = fmt.Sprintf("teacher_%s_%s.json", user.Username, base)
		} else {
			safeName = filepath.Base(editingFile)
		}
		outPath = filepath.Join("questions", safeName)
	} else {
		if customFilename != "" {
			base := filepath.Base(customFilename)
			base = strings.TrimSuffix(base, ".json")
			safeName = fmt.Sprintf("teacher_%s_%s.json", user.Username, base)
		} else {
			safeName = fmt.Sprintf("teacher_%s_%d.json", user.Username, time.Now().Unix())
		}
		outPath = filepath.Join("questions", safeName)
	}

	if err := os.WriteFile(outPath, data, 0644); err != nil {
		slog.Error("failed to save questions file", "error", err)
		http.Error(w, "failed to save file", http.StatusInternalServerError)
		return
	}

	if len(oldQuestions) > 0 {
		oldTexts := make([]string, 0, len(oldQuestions))
		for _, q := range oldQuestions {
			oldTexts = append(oldTexts, q.Text)
		}
		if err := h.store.DeleteQuestionsByTexts(1, oldTexts); err != nil {
			slog.Error("failed to remove old questions", "error", err)
			http.Error(w, "failed to update questions", http.StatusInternalServerError)
			return
		}
	}

	for _, qi := range questions {
		_, err := h.store.InsertQuestion(model.Question{
			CourseID:    1,
			Text:        qi.Text,
			Difficulty:  qi.Difficulty,
			Topic:       qi.Topic,
			Rubric:      qi.Rubric,
			ModelAnswer: qi.ModelAnswer,
			MaxPoints:   qi.MaxPoints,
		})
		if err != nil {
			slog.Error("failed to insert question", "error", err)
			http.Error(w, "failed to insert question: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	// Fix: Changed from 'filename' to 'safeName' to ensure the exact
	// disk destination filename is tracked in the database index.
	if err := h.store.SetImportedFileHash(safeName, hash); err != nil {
		slog.Warn("failed to set import hash", "error", err)
	}

	slog.Info("teacher uploaded questions", "user", user.Username, "file", outPath, "count", len(questions))

	// Automatically redirect to teacher's profile
	http.Redirect(w, r, "/teacher/profile", http.StatusSeeOther)
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
