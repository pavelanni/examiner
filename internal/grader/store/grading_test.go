package store_test

import (
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

func TestGetReviewData(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	if err := s.ImportExam(sampleExport()); err != nil {
		t.Fatalf("ImportExam: %v", err)
	}

	// Get session ID from student list
	students, _ := s.ListStudentsForExam("test-exam-1")
	if len(students) == 0 {
		t.Fatal("no students found")
	}
	sessionID := students[0].SessionID

	data, err := s.GetReviewData(sessionID)
	if err != nil {
		t.Fatalf("GetReviewData: %v", err)
	}
	if data == nil {
		t.Fatal("expected review data, got nil")
	}
	if data.StudentName != "Alice" {
		t.Errorf("StudentName = %q, want Alice", data.StudentName)
	}
	if data.ExamID != "test-exam-1" {
		t.Errorf("ExamID = %q, want test-exam-1", data.ExamID)
	}
	if len(data.Questions) != 1 {
		t.Fatalf("len(Questions) = %d, want 1", len(data.Questions))
	}
	q := data.Questions[0]
	if q.Text != "What is 2+2?" {
		t.Errorf("question text = %q", q.Text)
	}
	if len(q.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(q.Messages))
	}
	if q.LLMScore != 10 {
		t.Errorf("LLMScore = %f, want 10", q.LLMScore)
	}
}

func TestUpdateTeacherScore(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	s.ImportExam(sampleExport())

	students, _ := s.ListStudentsForExam("test-exam-1")
	sessionID := students[0].SessionID

	data, _ := s.GetReviewData(sessionID)
	questionID := data.Questions[0].ID

	err := s.UpdateTeacherScore(questionID, 8.5, "Good but missing detail")
	if err != nil {
		t.Fatalf("UpdateTeacherScore: %v", err)
	}

	// Verify the score was updated
	data, _ = s.GetReviewData(sessionID)
	if data.Questions[0].TeacherScore == nil {
		t.Fatal("expected teacher score, got nil")
	}
	if *data.Questions[0].TeacherScore != 8.5 {
		t.Errorf("TeacherScore = %f, want 8.5", *data.Questions[0].TeacherScore)
	}
	if data.Questions[0].TeacherComment != "Good but missing detail" {
		t.Errorf("TeacherComment = %q", data.Questions[0].TeacherComment)
	}
}

func TestFinalizeGrade(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	// Create a teacher user for reviewed_by
	s.CreateUser(model.User{
		Username:     "teacher",
		PasswordHash: "hash",
		Role:         model.UserRoleTeacher,
		Active:       true,
	})
	s.ImportExam(sampleExport())

	students, _ := s.ListStudentsForExam("test-exam-1")
	sessionID := students[0].SessionID

	err := s.FinalizeGrade(sessionID, 85.0, "Good work overall", 1)
	if err != nil {
		t.Fatalf("FinalizeGrade: %v", err)
	}

	// Verify grade and status
	data, _ := s.GetReviewData(sessionID)
	if data.Status != "reviewed" {
		t.Errorf("Status = %q, want reviewed", data.Status)
	}
	if data.Grade == nil {
		t.Fatal("expected grade info")
	}
	if data.Grade.FinalGrade == nil || *data.Grade.FinalGrade != 85.0 {
		t.Errorf("FinalGrade = %v, want 85.0", data.Grade.FinalGrade)
	}
	if data.Grade.TeacherComment != "Good work overall" {
		t.Errorf("TeacherComment = %q", data.Grade.TeacherComment)
	}
}
