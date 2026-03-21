package report_test

import (
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/grader/report"
	"github.com/pavelanni/examiner/internal/grader/store"
)

func TestGenerate(t *testing.T) {
	score := 8.0
	grade := 85.0
	data := store.ReviewData{
		Subject:     "Physics",
		ExamID:      "phys-2026",
		Date:        "2026-03-15",
		StudentName: "Alice Johnson",
		ExternalID:  "STU-001",
		LLMGrade:    92.7,
		Questions: []store.ReviewQuestion{
			{
				Position:       0,
				Text:           "What is gravity?",
				Topic:          "Mechanics",
				Difficulty:     "easy",
				MaxPoints:      10,
				LLMScore:       9,
				LLMFeedback:    "Good answer",
				TeacherScore:   &score,
				TeacherComment: "Needs more detail",
				Messages: []store.ConversationMessage{
					{Role: "student", Content: "Force of attraction"},
					{Role: "assistant", Content: "Correct, elaborate?"},
				},
			},
		},
		Grade: &store.GradeInfo{
			FinalGrade:     &grade,
			TeacherComment: "Good overall",
		},
	}

	md := report.Generate(data)

	checks := []string{
		"# Exam Report: Physics",
		"**Student:** Alice Johnson (STU-001)",
		"**LLM Grade:** 92.7%",
		"**Final Grade:** 85.0%",
		"Question 1: Mechanics",
		"Force of attraction",
		"| LLM | 9.0/10 |",
		"| Teacher | 8.0/10 |",
		"Good overall",
	}
	for _, c := range checks {
		if !strings.Contains(md, c) {
			t.Errorf("report missing %q", c)
		}
	}
}

func TestGenerateWithoutTeacherGrade(t *testing.T) {
	data := store.ReviewData{
		Subject:     "Math",
		ExamID:      "math-2026",
		Date:        "2026-03-15",
		StudentName: "Bob Smith",
		ExternalID:  "STU-002",
		LLMGrade:    75.0,
		Questions: []store.ReviewQuestion{
			{
				Text:        "What is pi?",
				Topic:       "Constants",
				Difficulty:  "easy",
				MaxPoints:   10,
				LLMScore:    7.5,
				LLMFeedback: "Partial answer",
			},
		},
	}

	md := report.Generate(data)

	if strings.Contains(md, "Final Grade") {
		t.Error("should not contain Final Grade when not set")
	}
	if strings.Contains(md, "Teacher") {
		t.Error("should not contain Teacher row when score not set")
	}
}
