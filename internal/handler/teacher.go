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

func teacherOwnedQuestionKeys(username string, oldQuestions []model.QuestionImport) map[string]struct{} {
	owned := make(map[string]struct{}, len(oldQuestions))
	for _, q := range oldQuestions {
		owned[questionDupKey(q.Topic, model.NormalizeQuestionText(q.Text))] = struct{}{}
	}
	for _, name := range listTeacherFiles(username) {
		b, err := os.ReadFile(filepath.Join("questions", name))
		if err != nil {
			continue
		}
		for _, q := range parseQuestionsFromJSON(b) {
			owned[questionDupKey(q.Topic, model.NormalizeQuestionText(q.Text))] = struct{}{}
		}
	}
	return owned
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

// handleTeacherUpload processes question file uploads and test definitions submitted by teachers.
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

	// Collect questions already owned by this teacher before writing outPath.
	ownedKeys := teacherOwnedQuestionKeys(user.Username, oldQuestions)

	var warnings []string
	var candidates []store.TeacherQuestionUploadItem
	seenInUpload := make(map[string]int, len(questions)) // topic + "\x00" + text_norm -> 1-based question number

	for i, qi := range questions {
		qNum := i + 1
		norm := model.NormalizeQuestionText(qi.Text)
		key := questionDupKey(qi.Topic, norm)

		// Check duplicate within the uploaded questions.
		if prevNum, seen := seenInUpload[key]; seen {
			warnings = append(warnings, appI18n.Td(r.Context(), "UploadDuplicateInFile", map[string]any{
				"Num":     qNum,
				"PrevNum": prevNum,
				"Topic":   qi.Topic,
			}))
			continue
		}
		seenInUpload[key] = qNum

		_, owned := ownedKeys[key]
		candidates = append(candidates, store.TeacherQuestionUploadItem{
			QuestionIndex: qNum,
			Owned:         owned,
			Question: model.Question{
				// TODO: derive course ID from context/config when multi-course support lands.
				CourseID:    1,
				Text:        qi.Text,
				TextNorm:    norm,
				Difficulty:  qi.Difficulty,
				Topic:       qi.Topic,
				Rubric:      qi.Rubric,
				ModelAnswer: qi.ModelAnswer,
				MaxPoints:   qi.MaxPoints,
			},
		})
	}

	oldTexts := make([]string, 0, len(oldQuestions))
	for _, q := range oldQuestions {
		oldTexts = append(oldTexts, q.Text)
	}

	savedIndices, skips, err := h.store.SaveTeacherQuestions(1, candidates, oldTexts)
	if err != nil {
		slog.Error("failed to save teacher questions", "error", err)
		http.Error(w, "failed to save questions", http.StatusInternalServerError)
		return
	}

	for _, skip := range skips {
		warnings = append(warnings, appI18n.Td(r.Context(), "UploadDuplicateInDB", map[string]any{
			"Num":   skip.QuestionIndex,
			"ID":    skip.ExistingID,
			"Topic": skip.Topic,
		}))
	}

	savedSet := make(map[int]struct{}, len(savedIndices))
	for _, idx := range savedIndices {
		savedSet[idx] = struct{}{}
	}
	savedQuestions := make([]model.QuestionImport, 0, len(savedIndices))
	for i, qi := range questions {
		if _, ok := savedSet[i+1]; ok {
			savedQuestions = append(savedQuestions, qi)
		}
	}
	savedCount := len(savedQuestions)

	if len(savedQuestions) > 0 {
		writtenData := data
		if len(savedQuestions) < len(questions) {
			encoded, err := json.MarshalIndent(savedQuestions, "", "  ")
			if err != nil {
				slog.Error("failed to encode saved questions", "error", err)
				http.Error(w, "failed to save file", http.StatusInternalServerError)
				return
			}
			writtenData = encoded
		}

		if err := os.WriteFile(outPath, writtenData, 0644); err != nil {
			slog.Error("failed to save questions file", "error", err)
			http.Error(w, "failed to save file", http.StatusInternalServerError)
			return
		}

		// Remove the old file when the edit flow renames it.
		if editingFile != "" && safeName != filepath.Base(editingFile) {
			oldPath := filepath.Join("questions", filepath.Base(editingFile))
			if err := os.Remove(oldPath); err != nil {
				slog.Warn("failed to remove old question file", "path", oldPath, "error", err)
			}
		}

		sum := sha256.Sum256(writtenData)
		hash := hex.EncodeToString(sum[:])

		// Fix: Changed from 'filename' to 'safeName' to ensure the exact
		// disk destination filename is tracked in the database index.
		if err := h.store.SetImportedFileHash(safeName, hash); err != nil {
			slog.Warn("failed to set import hash", "error", err)
		}
	}

	slog.Info("teacher uploaded questions", "user", user.Username, "file", outPath, "saved", savedCount, "warnings", len(warnings))

	if len(warnings) > 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		msg := formatUploadSummary(r.Context(), savedCount, warnings)
		if err := views.TeacherProfilePage(user.DisplayName, listTeacherFiles(user.Username), msg).Render(r.Context(), w); err != nil {
			slog.Error("render error", "error", err)
		}
		return
	}

	// Automatically redirect to teacher's profile
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
