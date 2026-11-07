package store

import (
	"database/sql"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// sessionEnv returns a store with a blueprint and three questions.
func sessionEnv(t *testing.T, s *Store) (bpID int64, qs []int64) {
	t.Helper()
	bpID, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam", MaxFollowups: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Q1", "Q2", "Q3"} {
		qs = append(qs, insertTestQuestion(t, s, text, "easy", "t"))
	}
	return bpID, qs
}

func threadQuestionIDs(t *testing.T, s *Store, sessionID int64) []int64 {
	t.Helper()
	threads, err := s.GetThreadsForSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, th := range threads {
		ids = append(ids, th.QuestionID)
	}
	return ids
}

func TestStartSessionPinsVariant(t *testing.T) {
	s := newTestStore(t)
	bp, q := sessionEnv(t, s)
	order := []int64{q[2], q[0], q[1]}

	sess, created, err := s.StartSession(bp, 7, "t", order, false)
	if err != nil || !created || sess.Topic != "t" || sess.Status != model.StatusInProgress {
		t.Fatalf("first start = %+v, %v, %v", sess, created, err)
	}
	if got := threadQuestionIDs(t, s, sess.ID); !slices.Equal(got, order) {
		t.Errorf("threads = %v, want the given order %v", got, order)
	}

	// Starting again, even with another draw, returns the same session.
	again, created, err := s.StartSession(bp, 7, "t", []int64{q[0]}, false)
	if err != nil || created || again.ID != sess.ID {
		t.Fatalf("second start = %+v, %v, %v; want session %d", again, created, err, sess.ID)
	}
	if got := threadQuestionIDs(t, s, sess.ID); !slices.Equal(got, order) {
		t.Errorf("threads after second start = %v, want %v", got, order)
	}
}

func TestStartSessionScope(t *testing.T) {
	s := newTestStore(t)
	bp, q := sessionEnv(t, s)
	first, _, err := s.StartSession(bp, 7, "t", q[:1], false)
	if err != nil {
		t.Fatal(err)
	}
	other, created, err := s.StartSession(bp, 8, "t", q[:1], false)
	if err != nil || !created || other.ID == first.ID {
		t.Errorf("another student: %+v, %v, %v; want a new session", other, created, err)
	}
	topic, created, err := s.StartSession(bp, 7, "u", q[:1], false)
	if err != nil || !created || topic.ID == first.ID {
		t.Errorf("another topic: %+v, %v, %v; want a new session", topic, created, err)
	}
	otherBP, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Another exam"})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, created, err := s.StartSession(otherBP, 7, "t", q[:1], false)
	if err != nil || !created || blueprint.ID == first.ID {
		t.Errorf("another blueprint: %+v, %v, %v; want a new session", blueprint, created, err)
	}
}

func TestStartSessionAfterFinish(t *testing.T) {
	s := newTestStore(t)
	bp, q := sessionEnv(t, s)
	first, _, err := s.StartSession(bp, 7, "t", q[:1], false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSessionStatus(first.ID, model.StatusGraded); err != nil {
		t.Fatal(err)
	}

	got, created, err := s.StartSession(bp, 7, "t", q[1:2], false)
	if err != nil || created || got.ID != first.ID || got.Status != model.StatusGraded {
		t.Errorf("no retake: %+v, %v, %v; want the finished session %d", got, created, err, first.ID)
	}
	got, created, err = s.StartSession(bp, 7, "t", q[1:2], true)
	if err != nil || !created || got.ID == first.ID {
		t.Errorf("retake: %+v, %v, %v; want a new session", got, created, err)
	}
	if found, err := s.FindExamSession(bp, 7, "t", false); err != nil || found == nil || found.ID != got.ID {
		t.Errorf("FindExamSession = %+v, %v; want the session in progress %d", found, err, got.ID)
	}
}

func TestRetakeWaitsForGrading(t *testing.T) {
	s := newTestStore(t)
	bp, qs := sessionEnv(t, s)
	first, _, err := s.StartSession(bp, 7, "t", qs[:1], true)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []model.SessionStatus{model.StatusSubmitted, model.StatusGrading} {
		if err := s.UpdateSessionStatus(first.ID, status); err != nil {
			t.Fatal(err)
		}
		again, created, err := s.StartSession(bp, 7, "t", qs[1:2], true)
		if err != nil || created || again.ID != first.ID || again.Status != status {
			t.Fatalf("retake during %s must reuse the pending attempt: %+v, %v, %v", status, again, created, err)
		}
	}
	// The existing LLM queue recovery can still restore the original
	// attempt without colliding with another in-progress session.
	if err := s.UpdateSessionStatus(first.ID, model.StatusInProgress); err != nil {
		t.Fatalf("queue recovery failed: %v", err)
	}
	if sessions, err := s.ListSessionsByUser(7); err != nil || len(sessions) != 1 {
		t.Fatalf("pending evaluation allowed a second attempt: %v, %v", sessions, err)
	}
}

func TestStartSessionConcurrent(t *testing.T) {
	// A file database: every pooled connection of :memory: is a separate DB.
	path := filepath.Join(t.TempDir(), "exam.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	bp, q := sessionEnv(t, s)
	// Independent handles also share the lock and uniqueness guarantee.
	other, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	stores := []*Store{s, other}

	const n = 10
	ids := make([]int64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			sess, _, err := stores[i%len(stores)].StartSession(bp, 7, "t", q, false)
			ids[i], errs[i] = sess.ID, err
		})
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("start %d got session %d, start 0 got %d", i, ids[i], ids[0])
		}
	}
	sessions, err := s.ListSessionsByUser(7)
	if err != nil || len(sessions) != 1 {
		t.Errorf("sessions = %v, %v; want exactly one", sessions, err)
	}
}

func TestStartSessionPinsVariantAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exam.db")
	open := func() *Store {
		t.Helper()
		s, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	s := open()
	bp, qs := sessionEnv(t, s)
	order := []int64{qs[2], qs[0], qs[1]}
	first, created, err := s.StartSession(bp, 7, "t", order, false)
	if err != nil || !created {
		t.Fatalf("first start: %+v, %v, %v", first, created, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = open()
	again, created, err := s.StartSession(bp, 7, "t", qs[:1], false)
	if err != nil || created || again.ID != first.ID || again.Topic != "t" {
		t.Fatalf("restart must resume the original variant: %+v, %v, %v", again, created, err)
	}
	if got := threadQuestionIDs(t, s, first.ID); !slices.Equal(got, order) {
		t.Fatalf("restart changed question order: %v, want %v", got, order)
	}
	if err := s.UpdateSessionStatus(first.ID, model.StatusGraded); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = open()
	finished, created, err := s.StartSession(bp, 7, "t", qs[:1], false)
	if err != nil || created || finished.ID != first.ID || finished.Status != model.StatusGraded {
		t.Fatalf("restart must preserve the completed attempt: %+v, %v, %v", finished, created, err)
	}
}

func TestOneActiveSessionIndex(t *testing.T) {
	s := newTestStore(t)
	bp, _ := sessionEnv(t, s)
	insert := func(topic string) error {
		_, err := s.db.Exec(`INSERT INTO exam_sessions (blueprint_id, student_id, topic, status, started_at) VALUES (?, 7, ?, 'in_progress', CURRENT_TIMESTAMP)`, bp, topic)
		return err
	}
	if err := insert("t"); err != nil {
		t.Fatal(err)
	}
	if err := insert("t"); err == nil {
		t.Error("second session in progress for the same exam: want a constraint error")
	}
	// Sessions created before topics were stored are left alone.
	for range 2 {
		if err := insert(""); err != nil {
			t.Errorf("legacy session without topic: %v", err)
		}
	}
}

func TestCreateSessionKeepsOrder(t *testing.T) {
	s := newTestStore(t)
	bp, q := sessionEnv(t, s)
	order := []int64{q[1], q[2], q[0]}
	id, err := s.CreateSession(bp, 7, order)
	if err != nil {
		t.Fatal(err)
	}
	if got := threadQuestionIDs(t, s, id); !slices.Equal(got, order) {
		t.Errorf("threads = %v, want %v", got, order)
	}
}

func TestMigrateLegacyExamVariants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Pre-#46 sessions may share the same student and blueprint. Neither
	// topic nor position exists yet; their historical order is thread ID.
	_, err = db.Exec(`
		CREATE TABLE exam_blueprints (id INTEGER PRIMARY KEY, course_id INTEGER NOT NULL DEFAULT 1,
		 name TEXT NOT NULL, time_limit INTEGER NOT NULL DEFAULT 0, max_followups INTEGER NOT NULL DEFAULT 3);
		CREATE TABLE questions (id INTEGER PRIMARY KEY, course_id INTEGER NOT NULL DEFAULT 1,
		 text TEXT NOT NULL, difficulty TEXT NOT NULL, topic TEXT NOT NULL,
		 rubric TEXT NOT NULL DEFAULT '', model_answer TEXT NOT NULL DEFAULT '', max_points INTEGER NOT NULL DEFAULT 10);
		CREATE TABLE exam_sessions (id INTEGER PRIMARY KEY, blueprint_id INTEGER NOT NULL,
		 student_id INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'in_progress',
		 started_at DATETIME NOT NULL, submitted_at DATETIME);
		CREATE TABLE question_threads (id INTEGER PRIMARY KEY, session_id INTEGER NOT NULL,
		 question_id INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'open');
		INSERT INTO exam_blueprints (id, name) VALUES (1, 'Exam');
		INSERT INTO questions (id, text, difficulty, topic) VALUES (1, 'Q1', 'easy', 't'), (2, 'Q2', 'easy', 't');
		INSERT INTO exam_sessions (id, blueprint_id, student_id, started_at) VALUES
		 (1, 1, 7, CURRENT_TIMESTAMP), (2, 1, 7, CURRENT_TIMESTAMP);
		INSERT INTO question_threads (id, session_id, question_id) VALUES (10, 1, 2), (20, 1, 1);
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sessions, err := s.ListSessionsByUser(7)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("legacy attempts must survive migration: %v, %v", sessions, err)
	}
	for _, sess := range sessions {
		if sess.Topic != "" || sess.Status != model.StatusInProgress {
			t.Fatalf("legacy attempt changed: %+v", sess)
		}
	}
	if got := threadQuestionIDs(t, s, 1); !slices.Equal(got, []int64{2, 1}) {
		t.Fatalf("legacy thread order changed: %v", got)
	}
	if _, created, err := s.StartSession(1, 7, "t", []int64{1, 2}, false); err != nil || !created {
		t.Fatalf("legacy sessions without a known topic must not break new starts: %v, %v", created, err)
	}
}
