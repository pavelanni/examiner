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

func ownerOf(t *testing.T, s *Store, id int64) int64 {
	t.Helper()
	q, err := s.GetQuestion(id)
	if err != nil {
		t.Fatalf("GetQuestion %d: %v", id, err)
	}
	return q.OwnerID
}

func owned(text, topic string, owner int64, published bool) model.Question {
	q := txQuestion(text, topic)
	q.OwnerID = owner
	q.Published = published
	return q
}

func TestFirstAdminID(t *testing.T) {
	s := newTestStore(t)
	createUser(t, s, "t", model.UserRoleTeacher)
	if _, err := s.FirstAdminID(); err == nil {
		t.Error("no admin: want error")
	}
	a := createUser(t, s, "a1", model.UserRoleAdmin)
	createUser(t, s, "a2", model.UserRoleAdmin)
	if got, err := s.FirstAdminID(); err != nil || got != a {
		t.Errorf("FirstAdminID = %d, %v; want %d", got, err, a)
	}
}

func TestFindUnownedQuestionIDs(t *testing.T) {
	s := newTestStore(t)
	tid := createUser(t, s, "t", model.UserRoleTeacher)
	free := txInsert(t, s, owned("A", "x", 0, true))
	txInsert(t, s, owned("A?", "x", tid, true)) // same key, owned
	ids, err := s.FindUnownedQuestionIDs("x", "a")
	if err != nil || len(ids) != 1 || ids[0] != free {
		t.Errorf("ids = %v, %v; want [%d]", ids, err, free)
	}
}

func TestClaimQuestionOwners(t *testing.T) {
	s := newTestStore(t)
	t1 := createUser(t, s, "t1", model.UserRoleTeacher)
	t2 := createUser(t, s, "t2", model.UserRoleTeacher)
	r1 := txInsert(t, s, owned("R1", "x", 0, true))
	r2 := txInsert(t, s, owned("R2", "x", 0, true))
	r3 := txInsert(t, s, owned("R3", "x", 0, true))
	r4 := txInsert(t, s, owned("R4", "x", t2, true))
	claims := map[int64][]int64{r1: {t1}, r2: {t1, t2}, r4: {t1}}

	if err := s.ClaimQuestionOwners(claims); err == nil {
		t.Fatal("no admin: want error")
	}
	if ownerOf(t, s, r1) != 0 {
		t.Fatal("rows changed although the claim failed")
	}

	admin := createUser(t, s, "admin", model.UserRoleAdmin)
	for run := 1; run <= 2; run++ {
		if err := s.ClaimQuestionOwners(claims); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		for id, want := range map[int64]int64{r1: t1, r2: admin, r3: admin, r4: t2} {
			if got := ownerOf(t, s, id); got != want {
				t.Errorf("run %d: row %d owner = %d, want %d", run, id, got, want)
			}
		}
	}
}

func TestClaimQuestionOwnersSameUserTwice(t *testing.T) {
	s := newTestStore(t)
	createUser(t, s, "admin", model.UserRoleAdmin)
	t1 := createUser(t, s, "t1", model.UserRoleTeacher)
	r1 := txInsert(t, s, owned("R1", "x", 0, true))
	if err := s.ClaimQuestionOwners(map[int64][]int64{r1: {t1, t1}}); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, s, r1); got != t1 {
		t.Errorf("owner = %d, want %d", got, t1)
	}
}

func TestAllPublished(t *testing.T) {
	s := newTestStore(t)
	t1 := createUser(t, s, "t1", model.UserRoleTeacher)
	t2 := createUser(t, s, "t2", model.UserRoleTeacher)
	txInsert(t, s, owned("X", "a", t1, true))
	txInsert(t, s, owned("Y", "b", t1, false))
	txInsert(t, s, owned("Z", "a", t2, false))
	tests := []struct {
		keys []QuestionKey
		want bool
	}{
		{[]QuestionKey{{"a", "x"}}, true},
		{[]QuestionKey{{"a", "x"}, {"b", "y"}}, false},
		{[]QuestionKey{{"zz", "zz"}}, true},
		{[]QuestionKey{{"a", "x"}, {"a", "z"}}, true}, // t2's unpublished row does not count
		{nil, true},
	}
	for _, tt := range tests {
		got, err := s.AllPublished(t1, tt.keys)
		if err != nil || got != tt.want {
			t.Errorf("AllPublished(%v) = %v, %v; want %v", tt.keys, got, err, tt.want)
		}
	}
}
