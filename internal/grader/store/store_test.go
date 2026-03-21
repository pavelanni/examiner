package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/grader/report"
	"github.com/pavelanni/examiner/internal/grader/store"
	"github.com/pavelanni/examiner/internal/model"
)

func TestFullGradingWorkflow(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// 1. Import sample exam
	exp := sampleExport()
	if err := s.ImportExam(exp); err != nil {
		t.Fatalf("ImportExam: %v", err)
	}

	// 2. List exams — verify count and metadata
	exams, err := s.ListExams()
	if err != nil {
		t.Fatalf("ListExams: %v", err)
	}
	if len(exams) != 1 {
		t.Fatalf("expected 1 exam, got %d", len(exams))
	}
	if exams[0].ReviewedCount != 0 {
		t.Errorf("expected 0 reviewed, got %d", exams[0].ReviewedCount)
	}

	// 3. List students — verify names
	students, err := s.ListStudentsForExam("test-exam-1")
	if err != nil {
		t.Fatalf("ListStudentsForExam: %v", err)
	}
	if len(students) != 1 {
		t.Fatalf("expected 1 student, got %d", len(students))
	}
	if students[0].DisplayName != "Alice" {
		t.Errorf("student name = %q, want Alice", students[0].DisplayName)
	}
	if students[0].Status != "imported" {
		t.Errorf("status = %q, want imported", students[0].Status)
	}
	sessionID := students[0].SessionID

	// 4. Get review data — verify questions and conversations
	data, err := s.GetReviewData(sessionID)
	if err != nil {
		t.Fatalf("GetReviewData: %v", err)
	}
	if data == nil {
		t.Fatal("expected review data")
		return
	}
	if len(data.Questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(data.Questions))
	}
	if len(data.Questions[0].Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(data.Questions[0].Messages))
	}

	// 5. Update teacher score — verify transition to in_review
	questionID := data.Questions[0].ID
	if err := s.UpdateTeacherScore(questionID, 9.0, "Well done"); err != nil {
		t.Fatalf("UpdateTeacherScore: %v", err)
	}
	if err := s.SetSessionStatus(sessionID, "in_review"); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}

	data, _ = s.GetReviewData(sessionID)
	if data.Status != "in_review" {
		t.Errorf("status after score = %q, want in_review", data.Status)
	}
	if data.Questions[0].TeacherScore == nil || *data.Questions[0].TeacherScore != 9.0 {
		t.Error("teacher score not saved correctly")
	}

	// 6. Finalize grade
	// Create a teacher user first
	if _, err := s.CreateUser(model.User{
		Username:     "teacher",
		PasswordHash: "hash",
		Role:         model.UserRoleTeacher,
		Active:       true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.FinalizeGrade(sessionID, 90.0, "Great work", 1); err != nil {
		t.Fatalf("FinalizeGrade: %v", err)
	}

	// 7. Verify session status = reviewed
	data, _ = s.GetReviewData(sessionID)
	if data.Status != "reviewed" {
		t.Errorf("status after finalize = %q, want reviewed", data.Status)
	}
	if data.Grade == nil || data.Grade.FinalGrade == nil || *data.Grade.FinalGrade != 90.0 {
		t.Error("final grade not saved correctly")
	}

	// 8. Verify exam shows as reviewed in listing
	exams, _ = s.ListExams()
	if exams[0].ReviewedCount != 1 {
		t.Errorf("reviewed count = %d, want 1", exams[0].ReviewedCount)
	}

	// 9. Generate report — verify content
	md := report.Generate(*data)
	if !strings.Contains(md, "Alice") {
		t.Error("report missing student name")
	}
	if !strings.Contains(md, "90.0%") {
		t.Error("report missing final grade")
	}
	if !strings.Contains(md, "Great work") {
		t.Error("report missing teacher comment")
	}
	if !strings.Contains(md, "9.0/10") {
		t.Error("report missing teacher score")
	}
}

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
		return
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

	if _, err := s.CreateUser(model.User{
		Username:     "admin",
		PasswordHash: "hash",
		Role:         model.UserRoleAdmin,
		Active:       true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

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
		return
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

func TestImportUsersCSV(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	csvData := "teacher_id,display_name\nT-001,Ivan Ivanov\nT-002,Petr Petrov\n"
	creds, err := s.ImportUsersCSV(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("ImportUsersCSV: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("imported %d users, want 2", len(creds))
	}

	// Verify generated credentials
	if creds[0].TeacherID != "T-001" {
		t.Errorf("TeacherID = %q, want T-001", creds[0].TeacherID)
	}
	if creds[0].Username != "iivanov" {
		t.Errorf("Username = %q, want iivanov", creds[0].Username)
	}
	if !strings.HasPrefix(creds[0].Password, "teach-") {
		t.Errorf("Password = %q, want teach-* prefix", creds[0].Password)
	}

	// Verify user was stored
	u, err := s.GetUserByUsername(creds[0].Username)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u == nil {
		t.Fatal("expected user, got nil")
		return
	}
	if u.DisplayName != "Ivan Ivanov" {
		t.Errorf("DisplayName = %q, want Ivan Ivanov", u.DisplayName)
	}
	if u.Role != model.UserRoleTeacher {
		t.Errorf("Role = %q, want teacher", u.Role)
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
		return
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
