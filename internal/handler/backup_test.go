package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/backup"
	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// backupStore returns an in-memory store with two users.
func backupStore(t *testing.T) *store.Store {
	t.Helper()
	if err := i18n.Init("en"); err != nil {
		t.Fatal(err)
	}
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, name := range []string{"admin", "student"} {
		if _, err := s.CreateUser(model.User{Username: name, PasswordHash: "x", Role: model.UserRoleStudent, Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestHealthz(t *testing.T) {
	s := backupStore(t)
	h := &Handler{store: s}
	rec := httptest.NewRecorder()
	h.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Errorf("healthy: %d %q", rec.Code, rec.Body.String())
	}

	_ = s.Close()
	rec = httptest.NewRecorder()
	h.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "sql") {
		t.Errorf("closed database: %d %q; want 503 without internal details", rec.Code, rec.Body.String())
	}
}

func backupRequest(t *testing.T, h *Handler, method string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/admin/backups", nil)
	admin := &model.User{ID: 1, DisplayName: "Admin", Role: model.UserRoleAdmin}
	req = req.WithContext(i18n.WithLocalizer(model.ContextWithUser(req.Context(), admin), i18n.NewLocalizer("en")))
	if method == http.MethodPost {
		h.handleCreateBackup(rec, req)
	} else {
		h.handleAdminBackupsPage(rec, req)
	}
	return rec
}

func TestAdminBackupsDisabled(t *testing.T) {
	h := &Handler{store: backupStore(t)}
	if rec := backupRequest(t, h, http.MethodGet); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("GET: %d; want the page saying backups are not configured", rec.Code)
	}
	if rec := backupRequest(t, h, http.MethodPost); rec.Code != http.StatusConflict {
		t.Errorf("POST: %d, want 409", rec.Code)
	}
}

func TestAdminCreateBackup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	s := backupStore(t)
	h := &Handler{store: s, config: model.ExamConfig{BackupDir: dir, BackupName: "examiner-en", BackupKeep: 14}}

	if body := backupRequest(t, h, http.MethodGet).Body.String(); !strings.Contains(body, "No backups yet.") {
		t.Errorf("empty list not shown")
	}
	rec := backupRequest(t, h, http.MethodPost)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "created and checked") {
		t.Fatalf("POST: %d; body lacks the confirmation", rec.Code)
	}
	list, err := backup.List(dir, "examiner-en")
	if err != nil || len(list) != 1 {
		t.Fatalf("backups = %v, %v; want one", list, err)
	}
	if !strings.Contains(rec.Body.String(), list[0].Name()) {
		t.Errorf("the new backup is not listed")
	}
	// The backup holds the server's data.
	restored := filepath.Join(t.TempDir(), "r.db")
	if _, err := backup.Restore(t.Context(), list[0].Path, restored, list[0].Time); err != nil {
		t.Fatal(err)
	}
	db, err := backup.OpenReadOnly(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if st, err := backup.Count(t.Context(), db); err != nil || st.Users != 2 {
		t.Errorf("restored stats = %+v, %v; want the 2 users of the store", st, err)
	}
}

func TestAdminCreateBackupFailure(t *testing.T) {
	s := backupStore(t)
	// A file where the directory should be makes the backup fail.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: s, config: model.ExamConfig{BackupDir: blocker, BackupName: "examiner-en"}}
	rec := backupRequest(t, h, http.MethodPost)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "server log") {
		t.Errorf("status %d; want 500 pointing to the log", rec.Code)
	}
	if strings.Contains(rec.Body.String(), blocker) && strings.Contains(rec.Body.String(), "not a directory") {
		t.Errorf("internal error leaked to the page")
	}
}
