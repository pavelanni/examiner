package store

import (
	"database/sql"

	"errors"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// TestSaveTeacherQuestionsRollsBackBeforePublication
// verifies that a database failure leaves no partial writes and prevents publication.
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
	_, _, err := s.SaveTeacherQuestionFile(1, []TeacherQuestionUploadItem{{QuestionIndex: 1, Question: q}, {QuestionIndex: 2, Question: rejected}}, nil, "", "test.json", func([]int) (string, error) {
		published = true
		return "hash", nil
	})
	if err == nil || published {
		t.Fatalf("error = %v, published = %v", err, published)
	}
	rows, err := s.ListQuestions()
	if err != nil || len(rows) != 0 {
		t.Fatalf("questions after rollback = %v, error = %v", rows, err)
	}
}

// TestSaveTeacherQuestionFileRollsBackOnPublishError covers a failure after
// the accepted questions have already been written inside the transaction.
func TestSaveTeacherQuestionFileRollsBackOnPublishError(t *testing.T) {
	s := newTestStore(t)
	q := model.Question{CourseID: 1, Text: "first", Difficulty: "easy", Topic: "t", MaxPoints: 1}
	publishErr := errors.New("test publication failure")
	_, _, err := s.SaveTeacherQuestionFile(1, []TeacherQuestionUploadItem{{QuestionIndex: 1, Question: q}}, nil, "", "test.json", func(indices []int) (string, error) {
		if len(indices) != 1 || indices[0] != 1 {
			t.Errorf("accepted indices = %v", indices)
		}
		return "", publishErr
	})
	if !errors.Is(err, publishErr) {
		t.Fatalf("error = %v, want publication failure", err)
	}
	rows, err := s.ListQuestions()
	if err != nil || len(rows) != 0 {
		t.Fatalf("questions after rollback = %v, error = %v", rows, err)
	}
}

// TestSaveTeacherQuestionsRemovesOnlyMatchingTopic covers equal text across topics.
func TestSaveTeacherQuestionsRemovesOnlyMatchingTopic(t *testing.T) {
	s := newTestStore(t)
	removed := insertTestQuestion(t, s, "Shared text", "easy", "old topic")
	preserved := insertTestQuestion(t, s, "Shared text", "easy", "other topic")
	q := model.Question{CourseID: 1, Text: "Replacement", Topic: "old topic", Difficulty: "easy", MaxPoints: 1}
	items := []TeacherQuestionUploadItem{{QuestionIndex: 1, Question: q}}
	old := []model.QuestionImport{{Text: "Shared text", Topic: "old topic"}}
	if _, _, err := s.SaveTeacherQuestions(1, items, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetQuestion(removed); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old question should be deleted: %v", err)
	}
	if _, err := s.GetQuestion(preserved); err != nil {
		t.Fatalf("same text in another topic should remain: %v", err)
	}
}
