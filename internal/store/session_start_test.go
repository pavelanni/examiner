package store

import (
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// sessionEnv returns a store with a blueprint and three published questions.
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

func TestStartSessionConcurrent(t *testing.T) {
	// A file database: every pooled connection of :memory: is a separate DB.
	s, err := New(filepath.Join(t.TempDir(), "exam.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	bp, q := sessionEnv(t, s)

	const n = 10
	ids := make([]int64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			sess, _, err := s.StartSession(bp, 7, "t", q, false)
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
