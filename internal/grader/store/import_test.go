package store_test

import (
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

func sampleExport() model.ExamExport {
	now := time.Now()
	score, grade := 10.0, 100.0
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
						LLMScore:    &score,
						LLMFeedback: "Perfect answer",
					},
				},
				LLMGrade: &grade,
			},
		},
	}
}

func TestImportExam(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close() }()

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
	defer func() { _ = s.Close() }()

	exp := sampleExport()
	if err := s.ImportExam(exp); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if err := s.ImportExam(exp); err == nil {
		t.Fatal("expected error on duplicate import, got nil")
	}
}

// Reset history remains in an exam export but must not become reviewable again.
func TestImportExamSkipsAnnulledAttempts(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close() }()
	exp := sampleExport()
	cancelled := exp.Results[0]
	cancelled.Status = model.StatusAnnulled
	cancelled.LLMGrade = nil
	valid := exp.Results[0]
	valid.SessionNumber = 2
	exp.Results = []model.StudentResult{cancelled, valid}
	if err := s.ImportExam(exp); err != nil {
		t.Fatal(err)
	}
	exams, err := s.ListExams()
	if err != nil || len(exams) != 1 || exams[0].StudentCount != 1 {
		t.Fatalf("cancelled attempt counted as a student: %+v, %v", exams, err)
	}
	items, err := s.ListStudentsForExam(exp.ExamID)
	if err != nil || len(items) != 1 || items[0].LLMGrade != *valid.LLMGrade {
		t.Fatalf("cancelled attempt imported: %+v, %v", items, err)
	}
}

func TestUnfinishedGradeDoesNotConsumeExamImport(t *testing.T) {
	for _, status := range []model.SessionStatus{model.StatusSubmitted, model.StatusGrading, model.StatusGradingFailed} {
		t.Run(string(status), func(t *testing.T) {
			s := newTestStore(t)
			defer func() { _ = s.Close() }()
			exp := sampleExport()
			pending := exp.Results[0]
			pending.Status = status
			pending.LLMGrade = nil
			exp.Results = append(exp.Results, pending)
			if err := s.ImportExam(exp); err == nil {
				t.Fatal("imported unpublished scores")
			}
			exams, err := s.ListExams()
			if err != nil || len(exams) != 0 {
				t.Fatalf("partial import: %+v, %v", exams, err)
			}
			if err := s.ImportExam(sampleExport()); err != nil {
				t.Fatalf("completed export cannot be imported: %v", err)
			}
		})
	}
}
