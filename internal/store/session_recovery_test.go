package store

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// TestRecoverInterruptedSessions preserves variants and grading data across a restart.
func TestRecoverInterruptedSessions(t *testing.T) {
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
	var before []*model.SessionView
	for i, status := range []model.SessionStatus{model.StatusInProgress, model.StatusSubmitted, model.StatusGrading, model.StatusGraded, model.StatusReviewed} {
		sess, _, err := s.StartSession(bp, int64(100+i), "t", order, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateSessionStatus(sess.ID, model.StatusSubmitted); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateSessionStatus(sess.ID, status); err != nil {
			t.Fatal(err)
		}
		threads, err := s.GetThreadsForSession(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddMessage(model.Message{ThreadID: threads[0].ID, Role: model.RoleStudent, Content: "saved answer"}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertScore(model.QuestionScore{ThreadID: threads[0].ID, LLMScore: 4, LLMFeedback: "saved feedback"}); err != nil {
			t.Fatal(err)
		}
		view, err := s.GetSessionView(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, view)
	}
	// Legacy attempts with unknown scopes can share a student and blueprint.
	for range 2 {
		id, err := s.CreateSession(bp, 7, order)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateSessionStatus(id, model.StatusGrading); err != nil {
			t.Fatal(err)
		}
		view, err := s.GetSessionView(id)
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, view)
	}
	// Merely opening another handle must not reset a live grading request.
	other := open()
	if live, err := other.GetSession(before[2].Session.ID); err != nil || live.Status != model.StatusGrading {
		t.Fatalf("opening a store reset active grading: %+v, %v", live, err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open()
	if n, err := s.RecoverInterruptedSessions(); err != nil || n != 4 {
		t.Fatalf("recovered = %d, %v; want 4", n, err)
	}
	for _, want := range before {
		if want.Session.Status == model.StatusSubmitted || want.Session.Status == model.StatusGrading {
			want.Session.Status = model.StatusInProgress
		}
		got, err := s.GetSessionView(want.Session.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("recovery changed data beyond status: got %+v, want %+v, error %v", got, want, err)
		}
		if ids := threadQuestionIDs(t, s, want.Session.ID); !slices.Equal(ids, order) {
			t.Fatalf("question order changed: %v, want %v", ids, order)
		}
	}
	if n, err := s.RecoverInterruptedSessions(); err != nil || n != 0 {
		t.Fatalf("repeated recovery = %d, %v; want 0", n, err)
	}
}

// TestRecoveryRollsBackOnConflictingScope rejects inconsistent data without partial recovery.
func TestRecoveryRollsBackOnConflictingScope(t *testing.T) {
	s := newTestStore(t)
	bp, qs := sessionEnv(t, s)
	first, _, err := s.StartSession(bp, 7, "t", qs, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSessionStatus(first.ID, model.StatusGrading); err != nil {
		t.Fatal(err)
	}
	// A conflicting active scope can exist in manually modified/older data.
	if _, err := s.db.Exec(`INSERT INTO exam_sessions (blueprint_id, student_id, topic, topic_known, status, started_at)
		VALUES (?, 7, 't', 1, 'in_progress', CURRENT_TIMESTAMP)`, bp); err != nil {
		t.Fatal(err)
	}
	other, _, err := s.StartSession(bp, 8, "t", qs, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSessionStatus(other.ID, model.StatusSubmitted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverInterruptedSessions(); err == nil {
		t.Fatal("conflicting recovery must report an error")
	}
	for id, want := range map[int64]model.SessionStatus{first.ID: model.StatusGrading, other.ID: model.StatusSubmitted} {
		got, err := s.GetSession(id)
		if err != nil || got.Status != want {
			t.Fatalf("partial recovery committed: %+v, %v; want %s", got, err, want)
		}
	}
}
