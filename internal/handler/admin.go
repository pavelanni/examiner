package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// handleAdminUsersPage serves the admin users management page.
func (h *Handler) handleAdminUsersPage(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers()
	if err != nil {
		slog.Error("failed to list users", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AdminUsersPage(users, "").Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

// handleCreateUser processes the user creation form.
func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	externalID := r.FormValue("external_id")
	displayName := r.FormValue("display_name")
	password := r.FormValue("password")
	role := r.FormValue("role")

	if username == "" || password == "" {
		http.Error(w, "username and password required", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("failed to hash password", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if displayName == "" {
		displayName = username
	}

	_, err = h.store.CreateUser(model.User{
		Username:     username,
		ExternalID:   externalID,
		DisplayName:  displayName,
		PasswordHash: string(hash),
		Role:         model.UserRole(role),
		Active:       true,
	})
	if errors.Is(err, store.ErrUsernameTaken) {
		http.Error(w, "username already exists", http.StatusConflict)
		return
	}
	if err != nil {
		slog.Error("failed to create user", "error", err)
		http.Error(w, "failed to create user", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.path("/admin/users"), http.StatusSeeOther)
}

// handleToggleUserActive переключает активность другого пользователя.
// Собственную учётную запись администратора отключить нельзя.
func (h *Handler) handleToggleUserActive(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "userID")
	if !ok {
		return
	}

	if current := model.UserFromContext(r.Context()); current != nil && current.ID == id {
		http.Error(w, appI18n.T(r.Context(), "CannotDeactivateSelf"), http.StatusForbidden)
		return
	}

	if err := h.store.ToggleUserActive(id); err != nil {
		lookupError(w, err, "failed to toggle user active", "user_id", id)
		return
	}

	http.Redirect(w, r, h.path("/admin/users"), http.StatusSeeOther)
}

func questionDupKey(topic, textNorm string) string {
	return topic + "\x00" + textNorm
}

func formatUploadSummary(ctx context.Context, count int, warnings []string) string {
	if len(warnings) > 0 {
		return appI18n.Td(ctx, "UploadSuccessWithWarnings", map[string]any{
			"Count":    count,
			"Warnings": strings.Join(warnings, " "),
		})
	}
	return appI18n.Td(ctx, "UploadSuccess", map[string]any{
		"Count": count,
	})
}

func (h *Handler) loadExistingDuplicates() []model.DuplicateQuestionGroup {
	dups, err := h.store.FindExistingDuplicates()
	if err != nil {
		slog.Error("failed to find existing duplicate questions", "error", err)
		return nil
	}
	return dups
}

// handleAdminQuestionsPage serves the admin questions management page.
func (h *Handler) handleAdminQuestionsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AdminQuestionsPage("", false, h.loadExistingDuplicates()).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

// handleUploadQuestions handles question file upload.
func (h *Handler) handleUploadQuestions(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "file too large", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("questions_file")
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}

	hashBytes := sha256.Sum256(data)
	hash := hex.EncodeToString(hashBytes[:])

	storedHash, err := h.store.GetImportedFileHash(header.Filename)
	if err != nil {
		slog.Error("failed to check import status", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if storedHash == hash {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := views.AdminQuestionsPage(appI18n.T(r.Context(), "UploadDuplicate"), true, h.loadExistingDuplicates()).Render(r.Context(), w); err != nil {
			slog.Error("render error", "error", err)
		}
		return
	}

	var questions []model.QuestionImport
	if err := json.Unmarshal(data, &questions); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	importedCount := 0
	var warnings []string
	seenInFile := make(map[string]int) // topic + "\x00" + text_norm -> 1-based question number

	for i, qi := range questions {
		qNum := i + 1
		norm := model.NormalizeQuestionText(qi.Text)
		key := questionDupKey(qi.Topic, norm)

		// Check duplicate within the same file (intra-file)
		if prevNum, duplicate := seenInFile[key]; duplicate {
			warnings = append(warnings, appI18n.Td(r.Context(), "UploadDuplicateInFile", map[string]any{
				"Num":     qNum,
				"PrevNum": prevNum,
				"Topic":   qi.Topic,
			}))
			continue
		}
		seenInFile[key] = qNum

		// Check duplicate against existing questions in DB (normalized)
		existing, err := h.store.FindQuestionByTopicAndTextNorm(qi.Topic, norm)
		if err != nil {
			slog.Error("failed to check question duplicate", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if existing != nil {
			warnings = append(warnings, appI18n.Td(r.Context(), "UploadDuplicateInDB", map[string]any{
				"Num":   qNum,
				"ID":    existing.ID,
				"Topic": qi.Topic,
			}))
			continue
		}

		// Insert question
		q := model.Question{
			CourseID:    1,
			Text:        qi.Text,
			TextNorm:    norm,
			Difficulty:  qi.Difficulty,
			Topic:       qi.Topic,
			Rubric:      qi.Rubric,
			ModelAnswer: qi.ModelAnswer,
			MaxPoints:   qi.MaxPoints,
		}
		id, err := h.store.InsertQuestion(q)
		if err != nil {
			slog.Error("failed to insert question", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if id > 0 {
			importedCount++
		}
	}

	if err := h.store.SetImportedFileHash(header.Filename, hash); err != nil {
		slog.Error("failed to record import", "error", err)
	}

	slog.Info("uploaded questions via admin", "filename", header.Filename, "imported", importedCount, "warnings", len(warnings))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	msg := formatUploadSummary(r.Context(), importedCount, warnings)
	if err := views.AdminQuestionsPage(msg, len(warnings) > 0, h.loadExistingDuplicates()).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}
