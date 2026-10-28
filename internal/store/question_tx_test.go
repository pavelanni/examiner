package store

import (
	"context"
	"errors"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

func txQuestion(text, topic string) model.Question {
	return model.Question{
		CourseID: 1, Text: text, Difficulty: model.DifficultyEasy,
		Topic: topic, Rubric: "r", ModelAnswer: "a", MaxPoints: 1,
	}
}

func countQuestions(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.QuestionCount()
	if err != nil {
		t.Fatalf("QuestionCount: %v", err)
	}
	return n
}

func TestInQuestionTxCommits(t *testing.T) {
	s := newTestStore(t)
	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		if _, err := tx.Insert(txQuestion("A", "x")); err != nil {
			return err
		}
		_, err := tx.Insert(txQuestion("B", "x"))
		return err
	})
	if err != nil {
		t.Fatalf("InQuestionTx: %v", err)
	}
	if n := countQuestions(t, s); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

func TestInQuestionTxRollsBackOnError(t *testing.T) {
	s := newTestStore(t)
	boom := errors.New("boom")
	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		if _, err := tx.Insert(txQuestion("A", "x")); err != nil {
			return err
		}
		if err := tx.SetImportedFileHash("f.json", "h"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if n := countQuestions(t, s); n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
	if h, _ := s.GetImportedFileHash("f.json"); h != "" {
		t.Errorf("file hash = %q, want empty", h)
	}
}

func TestInQuestionTxRollsBackOnConstraint(t *testing.T) {
	s := newTestStore(t)
	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		if _, err := tx.Insert(txQuestion("A", "x")); err != nil {
			return err
		}
		_, err := tx.Insert(txQuestion("A", "y")) // same (course_id, text)
		return err
	})
	if err == nil {
		t.Fatal("want constraint error, got nil")
	}
	if n := countQuestions(t, s); n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

func TestQuestionTxFinders(t *testing.T) {
	s := newTestStore(t)
	id := insertTestQuestion(t, s, "A", "easy", "x")
	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		q, err := tx.FindByTopicAndTextNorm("x", model.NormalizeQuestionText("  a? "))
		if err != nil || q == nil || q.ID != id {
			t.Errorf("FindByTopicAndTextNorm = %+v, %v; want id %d", q, err, id)
		}
		if q, err := tx.FindByTopicAndTextNorm("other", "a"); q != nil || err != nil {
			t.Errorf("miss: got %+v, %v; want nil, nil", q, err)
		}
		q, err = tx.FindByCourseAndText(1, "A")
		if err != nil || q == nil || q.ID != id || q.Topic != "x" {
			t.Errorf("FindByCourseAndText = %+v, %v; want id %d", q, err, id)
		}
		if q, err := tx.FindByCourseAndText(1, "a"); q != nil || err != nil {
			t.Errorf("exact-text miss: got %+v, %v; want nil, nil", q, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQuestionTxUpdate(t *testing.T) {
	s := newTestStore(t)
	id := insertTestQuestion(t, s, "A", "easy", "x")
	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		q := txQuestion("A", "x")
		q.ID = id
		q.Rubric = "new rubric"
		if err := tx.Update(q); err != nil {
			return err
		}
		q.ID = 9999
		if err := tx.Update(q); err == nil {
			t.Error("Update of a missing ID: want error, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetQuestion(id)
	if err != nil || got.Rubric != "new rubric" || got.TextNorm != "a" {
		t.Errorf("after update: %+v, %v", got, err)
	}
}

func TestQuestionTxDeleteUnusedByTexts(t *testing.T) {
	s := newTestStore(t)
	idKept := insertTestQuestion(t, s, "kept", "easy", "go")
	idRemoved := insertTestQuestion(t, s, "removed", "easy", "go")
	idReferenced := insertTestQuestion(t, s, "referenced", "easy", "go")
	bpID, err := s.CreateBlueprint(model.ExamBlueprint{Name: "test", TimeLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(bpID, 1, []int64{idReferenced}); err != nil {
		t.Fatal(err)
	}

	err = s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		return tx.DeleteUnusedByTexts(1, []string{"kept", "removed", "referenced"}, []string{"kept"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetQuestion(idKept); err != nil {
		t.Errorf("kept question deleted: %v", err)
	}
	if _, err := s.GetQuestion(idReferenced); err != nil {
		t.Errorf("referenced question deleted: %v", err)
	}
	if _, err := s.GetQuestion(idRemoved); err == nil {
		t.Error("unused question not deleted")
	}
}
