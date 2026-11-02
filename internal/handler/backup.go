package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/pavelanni/examiner/internal/backup"
	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
)

// handleHealthz reports whether the server and its database answer.
func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.store.Ping(ctx); err != nil {
		slog.Error("health check: database unavailable", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("database unavailable\n"))
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

func (h *Handler) handleAdminBackupsPage(w http.ResponseWriter, r *http.Request) {
	h.renderBackups(w, r, http.StatusOK, "", false)
}

// handleCreateBackup takes a backup of the running database (ТЗ п. 3.7.2:
// a manual backup before an exam).
func (h *Handler) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if h.config.BackupDir == "" {
		h.renderBackups(w, r, http.StatusConflict, appI18n.T(r.Context(), "BackupDisabled"), true)
		return
	}
	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	info, err := backup.Create(r.Context(), h.store, backup.Options{
		Dir:  h.config.BackupDir,
		Name: h.config.BackupName,
		Keep: h.config.BackupKeep,
	})
	if err != nil {
		slog.Error("backup from the admin page failed", "dir", h.config.BackupDir, "error", err)
		h.renderBackups(w, r, http.StatusInternalServerError, appI18n.T(r.Context(), "BackupFailed"), true)
		return
	}
	slog.Info("backup created from the admin page", "path", info.Path, "size", info.Size)
	h.renderBackups(w, r, http.StatusOK, appI18n.Td(r.Context(), "BackupCreated", map[string]any{"Name": info.Name()}), false)
}

func (h *Handler) renderBackups(w http.ResponseWriter, r *http.Request, status int, flash string, flashErr bool) {
	var list []backup.Info
	if h.config.BackupDir != "" {
		var err error
		if list, err = backup.List(h.config.BackupDir, h.config.BackupName); err != nil {
			slog.Error("failed to list backups", "dir", h.config.BackupDir, "error", err)
			if flash == "" {
				flash, flashErr = appI18n.T(r.Context(), "BackupListFailed"), true
			}
		}
	}
	page := views.BackupsPage{
		Enabled:  h.config.BackupDir != "",
		Dir:      h.config.BackupDir,
		Keep:     h.config.BackupKeep,
		Flash:    flash,
		FlashErr: flashErr,
	}
	for _, b := range list {
		page.Backups = append(page.Backups, views.BackupRow{Name: b.Name(), Time: b.Time, Size: b.Size, HasConfig: b.ConfigPath != ""})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := views.AdminBackupsPage(page).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}
