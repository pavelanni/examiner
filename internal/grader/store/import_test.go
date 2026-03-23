package store_test

import (
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

func sampleExport() model.ExamExport {
	now := time.Now()
	return model.ExamExport{
		ExamID:        "test-exam-1",
		Subject:       "Physics",
		Date:          "2026-03-15",
		PromptVariant: "standard",
		NumQuestions:  1,
		Results: []model.StudentResult{
			{
				ExternalID:    "STU-001",
				DisplayName:   "Alice",
				SessionNumber: 1,
				Status:        model.StatusGraded,
				StartedAt:     now,
				Questions: []model.QuestionResult{
					{
						Text:        "What is 2+2?",
						Topic:       "Arithmetic",
						Difficulty:  model.DifficultyEasy,
						MaxPoints:   10,
						Rubric:      "Answer is 4",
						ModelAnswer: "4",
						Conversation: []model.ConversationMsg{
							{Role: "student", Content: "4", At: now},
							{Role: "assistant", Content: "Correct!", At: now},
						},
						LLMScore:    10,
						LLMFeedback: "Perfect answer",
					},
				},
				LLMGrade: 100,
			},
		},
	}
}

func TestImportExam(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	exp := sampleExport()
	err := s.ImportExam(exp)
	if err != nil {
		t.Fatalf("ImportExam: %v", err)
	}

	exams, err := s.ListExams()
	if err != nil {
		t.Fatalf("ListExams: %v", err)
	}
	if len(exams) != 1 {
		t.Fatalf("len(exams) = %d, want 1", len(exams))
	}
	if exams[0].ExamID != "test-exam-1" {
		t.Errorf("ExamID = %q, want %q", exams[0].ExamID, "test-exam-1")
	}
	if exams[0].StudentCount != 1 {
		t.Errorf("StudentCount = %d, want 1", exams[0].StudentCount)
	}
}

func TestImportExamDuplicate(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	exp := sampleExport()
	if err := s.ImportExam(exp); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if err := s.ImportExam(exp); err == nil {
		t.Fatal("expected error on duplicate import, got nil")
	}
}
