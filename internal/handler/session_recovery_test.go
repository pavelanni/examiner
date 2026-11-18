package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// TestInterruptedGradingCanBeResubmitted completes the original variant after a restart.
func TestInterruptedGradingCanBeResubmitted(t *testing.T) {
	for _, status := range []model.SessionStatus{model.StatusSubmitted, model.StatusGrading} {
		t.Run(string(status), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "exam.db")
			s, err := store.New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			examStore(t, s, 4, 0, 0)
			calls := 0
			h := &Handler{store: s, config: model.ExamConfig{NumQuestions: 2, Shuffle: true}, llm: contextEvaluator{grade: func(context.Context) error {
				calls++
				return nil
			}}}
			id := startedSession(t, h, student(7), "t")
			wantIDs := questionIDs(sessionQuestions(t, s, id))
			threads, err := s.GetThreadsForSession(id)
			if err != nil {
				t.Fatal(err)
			}
			for _, th := range threads {
				if _, err := s.AddMessage(model.Message{ThreadID: th.ID, Role: model.RoleStudent, Content: "saved answer"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.UpdateSessionStatus(id, model.StatusSubmitted); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateSessionStatus(id, status); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			h.store = reopened
			router := chi.NewRouter()
			router.Post("/exam/{sessionID}/submit", h.handleSubmit)
			submit := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/exam/%d/submit", id), nil)
				router.ServeHTTP(rec, withExamUser(req, student(7)))
				return rec
			}
			if rec := submit(); rec.Code != http.StatusConflict || calls != 0 {
				t.Fatalf("without recovery the attempt should be stuck: status %d, grading calls %d", rec.Code, calls)
			}
			if n, err := reopened.RecoverInterruptedSessions(); err != nil || n != 1 {
				t.Fatalf("recovery = %d, %v; want 1", n, err)
			}
			if resumed := startedSession(t, h, student(7), "t"); resumed != id {
				t.Fatalf("recovery issued a different variant: %d, want %d", resumed, id)
			}
			if rec := submit(); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/results/%d", id) {
				t.Fatalf("resubmission failed: status %d, body %s", rec.Code, rec.Body.String())
			}
			for range threads {
				runNextEvaluation(t, h)
			}
			if calls != len(threads) {
				t.Fatalf("grading calls = %d, want %d", calls, len(threads))
			}
			if ids := questionIDs(sessionQuestions(t, reopened, id)); !slices.Equal(ids, wantIDs) {
				t.Fatalf("question order changed: %v, want %v", ids, wantIDs)
			}
			for _, th := range threads {
				messages, err := reopened.GetMessages(th.ID)
				if err != nil || len(messages) != 1 || messages[0].Content != "saved answer" {
					t.Fatalf("answers were lost or duplicated: %+v, %v", messages, err)
				}
			}
			finished, err := reopened.GetSession(id)
			if err != nil || finished.Status != model.StatusGraded {
				t.Fatalf("resubmission did not finish: %+v, %v", finished, err)
			}
			if rec := startExam(t, h, student(7), "t"); rec.Header().Get("Location") != fmt.Sprintf("/results/%d", id) {
				t.Fatalf("finished attempt permitted a reroll: %s", rec.Header().Get("Location"))
			}
			if sessions, err := reopened.ListSessionsByUser(7); err != nil || len(sessions) != 1 {
				t.Fatalf("a second variant was created: %+v, %v", sessions, err)
			}
		})
	}
}
