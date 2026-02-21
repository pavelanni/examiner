package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"golang.org/x/crypto/bcrypt"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/handler/views"
	"github.com/pavelanni/examiner/internal/model"
)

func (h *Handler) handleAdminUsersPage(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers()
	if err != nil {
		slog.Error("failed to list users", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AdminUsersPage(users, "").Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
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
		DisplayName:  displayName,
		PasswordHash: string(hash),
		Role:         model.UserRole(role),
		Active:       true,
	})
	if err != nil {
		slog.Error("failed to create user", "error", err)
		http.Error(w, "failed to create user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *Handler) handleToggleUserActive(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "userID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	if err := h.store.ToggleUserActive(id); err != nil {
		slog.Error("failed to toggle user active", "id", id, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *Handler) handleAdminQuestionsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AdminQuestionsPage("", false).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

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
	defer file.Close()

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
		if err := views.AdminQuestionsPage("UploadDuplicate", true).Render(r.Context(), w); err != nil {
			slog.Error("render error", "error", err)
		}
		return
	}

	var questions []model.QuestionImport
	if err := json.Unmarshal(data, &questions); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
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

	if err := h.store.SetImportedFileHash(header.Filename, hash); err != nil {
		slog.Error("failed to record import", "error", err)
	}

	slog.Info("uploaded questions via admin", "filename", header.Filename, "count", len(questions))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	msg := fmt.Sprintf("Successfully imported %d questions.", len(questions))
	if err := views.AdminQuestionsPage(msg, false).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}
