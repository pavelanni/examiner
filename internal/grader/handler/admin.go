package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/pavelanni/examiner/internal/grader/handler/views"
	"github.com/pavelanni/examiner/internal/model"
)

// uploadPage renders the exam upload form.
func (h *Handler) uploadPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AdminUploadPage("").Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

// handleUpload processes uploaded exam JSON files.
func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	const maxUploadSize = 32 << 20 // 32 MB
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		slog.Error("parse multipart form", "error", err)
		h.renderUploadPage(w, r, "Failed to parse upload: "+err.Error())
		return
	}

	var successes, errors []string

	for _, fh := range r.MultipartForm.File["files"] {
		f, err := fh.Open()
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: failed to open: %v", fh.Filename, err))
			continue
		}

		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: failed to read: %v", fh.Filename, err))
			continue
		}

		var export model.ExamExport
		if err := json.Unmarshal(data, &export); err != nil {
			errors = append(errors, fmt.Sprintf("%s: invalid JSON: %v", fh.Filename, err))
			continue
		}

		if err := h.store.ImportExam(export); err != nil {
			errors = append(errors, fmt.Sprintf("%s: import failed: %v", fh.Filename, err))
			continue
		}

		successes = append(successes, fmt.Sprintf("%s: imported successfully", fh.Filename))
	}

	var parts []string
	parts = append(parts, successes...)
	parts = append(parts, errors...)
	msg := strings.Join(parts, "; ")

	h.renderUploadPage(w, r, msg)
}

// renderUploadPage renders the upload page with a flash message.
func (h *Handler) renderUploadPage(w http.ResponseWriter, r *http.Request, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AdminUploadPage(msg).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

// usersPage renders the user management page.
func (h *Handler) usersPage(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers()
	if err != nil {
		slog.Error("list users", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AdminUsersPage(users, "").Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

// handleCreateUser processes the create-user form submission.
func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	displayName := r.FormValue("display_name")
	password := r.FormValue("password")
	role := r.FormValue("role")

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("hash password", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	u := model.User{
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: string(hash),
		Role:         model.UserRole(role),
		Active:       true,
	}
	if _, err := h.store.CreateUser(u); err != nil {
		slog.Error("create user", "error", err)
		http.Error(w, "failed to create user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// handleImportUsers processes a CSV upload of teacher accounts.
func (h *Handler) handleImportUsers(w http.ResponseWriter, r *http.Request) {
	const maxUploadSize = 1 << 20 // 1 MB
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		slog.Error("parse multipart form", "error", err)
		h.renderUsersPage(w, r, "Failed to parse upload: "+err.Error())
		return
	}

	files := r.MultipartForm.File["csv_file"]
	if len(files) == 0 {
		h.renderUsersPage(w, r, "No file selected")
		return
	}

	f, err := files[0].Open()
	if err != nil {
		h.renderUsersPage(w, r, "Failed to open file: "+err.Error())
		return
	}
	defer f.Close()

	n, err := h.store.ImportUsersCSV(f)
	if err != nil {
		h.renderUsersPage(w, r, fmt.Sprintf("Import error: %v", err))
		return
	}

	h.renderUsersPage(w, r, fmt.Sprintf("Imported %d teacher account(s)", n))
}

// renderUsersPage renders the users page with a flash message.
func (h *Handler) renderUsersPage(w http.ResponseWriter, r *http.Request, msg string) {
	users, err := h.store.ListUsers()
	if err != nil {
		slog.Error("list users", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AdminUsersPage(users, msg).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

// handleToggleUser toggles a user's active status.
func (h *Handler) handleToggleUser(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "userID")
	userID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid user ID", http.StatusBadRequest)
		return
	}

	if err := h.store.ToggleUserActive(userID); err != nil {
		slog.Error("toggle user active", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// handleDeleteExam deletes an exam and all related data.
func (h *Handler) handleDeleteExam(w http.ResponseWriter, r *http.Request) {
	examID := chi.URLParam(r, "examID")

	if err := h.store.DeleteExam(examID); err != nil {
		slog.Error("delete exam", "error", err)
		http.Error(w, "failed to delete exam: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}
