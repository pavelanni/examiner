package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"golang.org/x/crypto/bcrypt"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/questionimport"
	"github.com/pavelanni/examiner/internal/store"
)

// handleAdminUsersPage serves the admin users management page.
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
	if err != nil {
		slog.Error("failed to create user", "error", err)
		http.Error(w, "failed to create user: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.path("/admin/users"), http.StatusSeeOther)
}

// handleToggleUserActive toggles a user's active status.
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

	http.Redirect(w, r, h.path("/admin/users"), http.StatusSeeOther)
}

// handleAdminQuestionsPage serves the admin questions management page.
func (h *Handler) handleAdminQuestionsPage(w http.ResponseWriter, r *http.Request) {
	renderAdminQuestions(w, r, http.StatusOK, "", false, nil, nil)
}

func renderAdminQuestions(w http.ResponseWriter, r *http.Request, status int, flash string, flashErr bool, errs, warnings []string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := views.AdminQuestionsPage(flash, flashErr, errs, warnings).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

// handleUploadQuestions imports a question file. The whole file is
// validated first; a file with any error writes nothing.
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
		renderAdminQuestions(w, r, http.StatusOK, appI18n.T(r.Context(), "UploadDuplicate"), true, nil, nil)
		return
	}

	drafts, errs := questionimport.Parse(data)
	if len(errs) > 0 {
		renderAdminQuestions(w, r, http.StatusBadRequest, "", false, questionimport.Localize(r.Context(), errs), nil)
		return
	}

	var res questionimport.Result
	err = h.store.InQuestionTx(r.Context(), func(tx *store.QuestionTx) error {
		var err error
		res, err = questionimport.Apply(tx, drafts, questionimport.Options{
			Mode:      questionimport.ModeSkipExisting,
			CourseID:  1,
			File:      &questionimport.FileRecord{Name: header.Filename, Hash: hash},
			Actor:     model.UserFromContext(r.Context()),
			Published: true,
		})
		return err
	})
	if err != nil {
		slog.Error("failed to import questions", "filename", header.Filename, "error", err)
		renderAdminQuestions(w, r, http.StatusInternalServerError, appI18n.T(r.Context(), "QImportErrInternal"), true, nil, nil)
		return
	}

	slog.Info("uploaded questions via admin", "filename", header.Filename,
		"inserted", res.Inserted, "skipped", res.Skipped)

	summary := appI18n.Td(r.Context(), "QImportSummary", map[string]any{
		"Inserted": res.Inserted, "Updated": res.Updated, "Skipped": res.Skipped,
	})
	renderAdminQuestions(w, r, http.StatusOK, summary, false, nil, questionimport.LocalizeWarnings(r.Context(), res.Warnings))
}
