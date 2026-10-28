package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/sqlitedb"
)

func createUser(t *testing.T, s *Store, username string, role model.UserRole) int64 {
	t.Helper()
	id, err := s.CreateUser(model.User{Username: username, DisplayName: username, PasswordHash: "x", Role: role, Active: true})
	if err != nil {
		t.Fatalf("CreateUser %s: %v", username, err)
	}
	return id
}

func txInsert(t *testing.T, s *Store, q model.Question) int64 {
	t.Helper()
	var id int64
	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		var err error
		id, err = tx.Insert(q)
		return err
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return id
}

func TestMigrationAddsOwnershipColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE questions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		course_id INTEGER NOT NULL DEFAULT 1,
		text TEXT NOT NULL,
		text_norm TEXT NOT NULL DEFAULT '',
		difficulty TEXT NOT NULL,
		topic TEXT NOT NULL,
		rubric TEXT NOT NULL DEFAULT '',
		model_answer TEXT NOT NULL DEFAULT '',
		max_points INTEGER NOT NULL DEFAULT 10
	);
	INSERT INTO questions (text, text_norm, difficulty, topic) VALUES ('Legacy', 'legacy', 'easy', 't');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	for run := 1; run <= 2; run++ {
		s, err := New(path)
		if err != nil {
			t.Fatalf("New (run %d): %v", run, err)
		}
		q, err := s.GetQuestion(1)
		if err != nil {
			t.Fatalf("GetQuestion: %v", err)
		}
		if !q.Published || q.OwnerID != 0 {
			t.Errorf("run %d: legacy row = published %v, owner %d; want published, no owner", run, q.Published, q.OwnerID)
		}
		var hasTimes bool
		if err := s.db.QueryRow(`SELECT created_at IS NOT NULL AND updated_at IS NOT NULL FROM questions WHERE id = 1`).Scan(&hasTimes); err != nil || !hasTimes {
			t.Errorf("run %d: timestamps not set: %v", run, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQuestionOwnershipRoundTrip(t *testing.T) {
	s := newTestStore(t)
	uid := createUser(t, s, "ivan", model.UserRoleTeacher)
	q := txQuestion("A", "x")
	q.OwnerID = uid
	q.Published = false
	id := txInsert(t, s, q)

	got, err := s.GetQuestion(id)
	if err != nil || got.OwnerID != uid || got.Published {
		t.Errorf("GetQuestion = owner %d, published %v, %v; want owner %d, unpublished", got.OwnerID, got.Published, err, uid)
	}
	all, err := s.ListQuestions()
	if err != nil || len(all) != 1 || all[0].OwnerID != uid || all[0].Published {
		t.Errorf("ListQuestions = %+v, %v", all, err)
	}
}

func TestQuestionTxUpdateKeepsOwner(t *testing.T) {
	s := newTestStore(t)
	uid := createUser(t, s, "ivan", model.UserRoleTeacher)
	q := txQuestion("A", "x")
	q.OwnerID = uid
	q.Published = true
	id := txInsert(t, s, q)

	err := s.InQuestionTx(context.Background(), func(tx *QuestionTx) error {
		u := txQuestion("A", "x")
		u.ID = id
		u.OwnerID = 0
		u.Published = false
		return tx.Update(u)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetQuestion(id)
	if err != nil || got.OwnerID != uid || got.Published {
		t.Errorf("after update: owner %d, published %v, %v; want owner %d, unpublished", got.OwnerID, got.Published, err, uid)
	}
	var ordered bool
	if err := s.db.QueryRow(`SELECT updated_at >= created_at FROM questions WHERE id = ?`, id).Scan(&ordered); err != nil || !ordered {
		t.Errorf("updated_at not >= created_at: %v", err)
	}
}

func TestListPublishedTopicsSkipsUnpublished(t *testing.T) {
	s := newTestStore(t)
	a := txQuestion("A1", "a")
	a.Published = true
	txInsert(t, s, a)
	txInsert(t, s, txQuestion("B1", "b")) // unpublished
	topics, err := s.ListPublishedTopics()
	if err != nil || len(topics) != 1 || topics[0] != "a" {
		t.Errorf("topics = %v, %v; want [a]", topics, err)
	}
}

func TestListQuestionsFilteredSkipsUnpublished(t *testing.T) {
	s := newTestStore(t)
	p := txQuestion("P", "a")
	p.Published = true
	txInsert(t, s, p)
	txInsert(t, s, txQuestion("U", "a"))
	qs, err := s.ListQuestionsFiltered("", "a")
	if err != nil || len(qs) != 1 || qs[0].Text != "P" {
		t.Errorf("questions = %+v, %v; want only P", qs, err)
	}
}
