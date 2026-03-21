package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pavelanni/examiner/internal/grader/store"
	"github.com/pavelanni/examiner/internal/model"
)

func TestNew(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
}

// newTestStore is a helper for creating a test store instance.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestCreateAndGetUser(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	id, err := s.CreateUser(model.User{
		Username:     "teacher1",
		DisplayName:  "Test Teacher",
		PasswordHash: "hash123",
		Role:         model.UserRoleTeacher,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	u, err := s.GetUserByUsername("teacher1")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u == nil {
		t.Fatal("expected user, got nil")
	}
	if u.DisplayName != "Test Teacher" {
		t.Errorf("DisplayName = %q, want %q", u.DisplayName, "Test Teacher")
	}
}

func TestUserCount(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	n, err := s.UserCount()
	if err != nil {
		t.Fatalf("UserCount: %v", err)
	}
	if n != 0 {
		t.Errorf("UserCount = %d, want 0", n)
	}
}

func TestAuthSession(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	s.CreateUser(model.User{
		Username:     "admin",
		PasswordHash: "hash",
		Role:         model.UserRoleAdmin,
		Active:       true,
	})

	token, err := s.CreateAuthSession(1)
	if err != nil {
		t.Fatalf("CreateAuthSession: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	sess, err := s.GetAuthSession(token)
	if err != nil {
		t.Fatalf("GetAuthSession: %v", err)
	}
	if sess == nil {
		t.Fatal("expected session, got nil")
	}
	if sess.UserID != 1 {
		t.Errorf("UserID = %d, want 1", sess.UserID)
	}

	err = s.DeleteAuthSession(token)
	if err != nil {
		t.Fatalf("DeleteAuthSession: %v", err)
	}

	sess, err = s.GetAuthSession(token)
	if err != nil {
		t.Fatalf("GetAuthSession after delete: %v", err)
	}
	if sess != nil {
		t.Fatal("expected nil session after delete")
	}
}

func TestListStudentsForExam(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	if err := s.ImportExam(sampleExport()); err != nil {
		t.Fatalf("ImportExam: %v", err)
	}

	students, err := s.ListStudentsForExam("test-exam-1")
	if err != nil {
		t.Fatalf("ListStudentsForExam: %v", err)
	}
	if len(students) != 1 {
		t.Fatalf("len = %d, want 1", len(students))
	}
	if students[0].DisplayName != "Alice" {
		t.Errorf("name = %q, want Alice", students[0].DisplayName)
	}
}

func TestDeleteExam(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	if err := s.ImportExam(sampleExport()); err != nil {
		t.Fatalf("ImportExam: %v", err)
	}

	err := s.DeleteExam("test-exam-1")
	if err != nil {
		t.Fatalf("DeleteExam: %v", err)
	}
	exams, _ := s.ListExams()
	if len(exams) != 0 {
		t.Fatalf("len(exams) = %d after delete, want 0", len(exams))
	}
}

func TestGetExamByID(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	if err := s.ImportExam(sampleExport()); err != nil {
		t.Fatalf("ImportExam: %v", err)
	}

	ex, err := s.GetExamByID("test-exam-1")
	if err != nil {
		t.Fatalf("GetExamByID: %v", err)
	}
	if ex == nil {
		t.Fatal("expected exam, got nil")
	}
	if ex.Subject != "Physics" {
		t.Errorf("Subject = %q, want Physics", ex.Subject)
	}

	ex, err = s.GetExamByID("nonexistent")
	if err != nil {
		t.Fatalf("GetExamByID nonexistent: %v", err)
	}
	if ex != nil {
		t.Fatal("expected nil for nonexistent exam")
	}
}
