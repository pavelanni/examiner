package store

import (
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

func TestSaveTeacherQuestionsRollsBackBeforePublication(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_question BEFORE INSERT ON questions
		WHEN NEW.text = 'rejected' BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	q := model.Question{CourseID: 1, Text: "first", Difficulty: "easy", Topic: "t", MaxPoints: 1}
	rejected := q
	rejected.Text = "rejected"
	published := false
	err := s.SaveTeacherQuestions([]model.Question{q, rejected}, nil, "", "test.json", "hash", func() error {
		published = true
		return nil
	})
	if err == nil || published {
		t.Fatalf("error = %v, published = %v", err, published)
	}
	rows, err := s.ListQuestions()
	if err != nil || len(rows) != 0 {
		t.Fatalf("questions after rollback = %v, error = %v", rows, err)
	}
}
