package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/questionimport"
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
		if _, err := tx.Insert(txQuestion("A?", "x")); err != nil {
			return err
		}
		_, err := tx.Insert(txQuestion("a", "x")) // same active identity
		return err
	})
	if err == nil {
		t.Fatal("want constraint error, got nil")
	}
	if n := countQuestions(t, s); n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

// archive marks a question as a historical row, as the identity migration
// does for legacy duplicates.
func archive(t *testing.T, s *Store, id int64) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE questions SET archived = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionTxFinders(t *testing.T) {
	s := newTestStore(t)
	archived := insertTestQuestion(t, s, "A", "easy", "x")
	archive(t, s, archived)
	id := insertTestQuestion(t, s, "A.", "easy", "x")
	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		q, err := tx.FindByTopicAndTextNorm(1, "x", model.NormalizeQuestionText("  a? "), 0)
		if err != nil || q == nil || q.ID != id {
			t.Errorf("FindByTopicAndTextNorm = %+v, %v; want active id %d", q, err, id)
		}
		if q, err := tx.FindByTopicAndTextNorm(1, "x", "a", id); q != nil || err != nil {
			t.Errorf("excluding the active row: got %+v, %v; want nil, nil (archived rows do not count)", q, err)
		}
		if q, err := tx.FindByTopicAndTextNorm(1, "other", "a", 0); q != nil || err != nil {
			t.Errorf("other topic: got %+v, %v; want nil, nil", q, err)
		}
		if q, err := tx.FindByTopicAndTextNorm(2, "x", "a", 0); q != nil || err != nil {
			t.Errorf("other course: got %+v, %v; want nil, nil", q, err)
		}
		if q, err := tx.FindByID(id); err != nil || q == nil || q.Text != "A." {
			t.Errorf("FindByID(%d) = %+v, %v", id, q, err)
		}
		if q, err := tx.FindByID(archived); q != nil || err != nil {
			t.Errorf("FindByID(archived) = %+v, %v; want nil, nil", q, err)
		}
		if q, err := tx.FindByID(9999); q != nil || err != nil {
			t.Errorf("FindByID miss: got %+v, %v; want nil, nil", q, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQuestionTxUpdate(t *testing.T) {
	s := newTestStore(t)
	old := insertTestQuestion(t, s, "Old", "easy", "x")
	archive(t, s, old)
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
		q.ID = old
		if err := tx.Update(q); err == nil {
			t.Error("Update of an archived question: want error, got nil")
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

func TestQuestionTxDeleteUnusedByIDs(t *testing.T) {
	s := newTestStore(t)
	owner := createUser(t, s, "t", model.UserRoleTeacher)
	other := createUser(t, s, "o", model.UserRoleTeacher)
	idRemoved := txInsert(t, s, owned("removed", "go", owner, true))
	idKept := txInsert(t, s, owned("kept", "go", owner, true))
	idReferenced := txInsert(t, s, owned("referenced", "go", owner, true))
	idOthers := txInsert(t, s, owned("theirs", "go", other, true))
	idArchived := txInsert(t, s, owned("old", "go", owner, true))
	archive(t, s, idArchived)
	bpID, err := s.CreateBlueprint(model.ExamBlueprint{Name: "test", TimeLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession(bpID, 1, []int64{idReferenced}); err != nil {
		t.Fatal(err)
	}

	err = s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		return tx.DeleteUnusedByIDs(owner, []int64{idRemoved, idReferenced, idOthers, idArchived, 9999})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetQuestion(idRemoved); err == nil {
		t.Error("unused question not deleted")
	}
	for name, id := range map[string]int64{"kept": idKept, "referenced": idReferenced, "another owner's": idOthers, "archived": idArchived} {
		if _, err := s.GetQuestion(id); err != nil {
			t.Errorf("%s question deleted: %v", name, err)
		}
	}
}

func TestQuestionTxDeleteImportedFileHash(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetImportedFileHash("old.json", "h"); err != nil {
		t.Fatal(err)
	}
	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		return tx.DeleteImportedFileHash("old.json")
	})
	if err != nil {
		t.Fatal(err)
	}
	if h, err := s.GetImportedFileHash("old.json"); err != nil || h != "" {
		t.Errorf("hash = %q, %v; want none", h, err)
	}
}

var _ questionimport.Tx = (*QuestionTx)(nil)

func TestInQuestionTxReleasesLockOnPanic(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	func() {
		defer func() { _ = recover() }()
		_ = s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
			if _, err := tx.Insert(txQuestion("A", "x")); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	// A leaked BEGIN IMMEDIATE would hold the write lock until busy_timeout.
	if _, err := s.InsertQuestion(txQuestion("B", "x")); err != nil {
		t.Fatalf("write after panic: %v", err)
	}
	if n := countQuestions(t, s); n != 1 {
		t.Errorf("count = %d, want 1 (panicked insert rolled back)", n)
	}
}

func TestQuestionTxUserDisplayName(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateUser(model.User{Username: "ivan", DisplayName: "Ivanov", PasswordHash: "x", Role: model.UserRoleTeacher, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	err = s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		if name, err := tx.UserDisplayName(id); err != nil || name != "Ivanov" {
			t.Errorf("UserDisplayName(%d) = %q, %v", id, name, err)
		}
		if name, err := tx.UserDisplayName(9999); err != nil || name != "" {
			t.Errorf("unknown user = %q, %v; want empty, nil", name, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
