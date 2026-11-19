package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
)

// Exercise the real retry policy and dispatcher with a controlled HTTP provider.
// A slow first call also keeps both submit requests independent of the model.
func TestFinalGradingRetriesAndStaffRecovery(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent=%t", persistent), func(t *testing.T) {
			env := newRBACEnv(t)
			var calls atomic.Int32
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if n <= 2 || (persistent && n == 3) {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"message":"private provider detail"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"score\":7,\"max_points\":10,\"feedback\":\"result\"}"}}]}`)
			}))
			t.Cleanup(upstream.Close)
			client, err := llm.New(upstream.URL, "test", "fake", "standard", llm.WithMaxRetries(2))
			if err != nil {
				t.Fatal(err)
			}
			env.h.llm = client
			if _, err := env.store.AddMessage(model.Message{ThreadID: env.threadA, Role: model.RoleStudent, Content: "saved"}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(env.h.StartWorkers(t.Context(), 2))
			submit := fmt.Sprintf("/exam/%d/submit", env.sessA)
			results := fmt.Sprintf("/results/%d", env.sessA)
			start := time.Now()
			codes := make(chan int, 2)
			for range 2 {
				go func() { codes <- env.do("studentA", http.MethodPost, submit, nil).Code }()
			}
			seen := map[int]int{}
			for range 2 {
				select {
				case code := <-codes:
					seen[code]++
				case <-time.After(time.Second):
					t.Fatal("submit waited for the model")
				}
			}
			if seen[303] != 1 || seen[409] != 1 || time.Since(start) >= time.Second {
				t.Fatalf("concurrent submission: %v after %s", seen, time.Since(start))
			}
			if page := env.do("studentA", http.MethodGet, results, nil); !strings.Contains(page.Body.String(), "every 2s") {
				t.Fatal("pending result does not poll")
			}
			close(release)
			want := model.StatusGraded
			if persistent {
				want = model.StatusGradingFailed
			}
			waitForState(t, func() bool {
				s, err := env.store.GetSession(env.sessA)
				return err == nil && s.Status == want
			})
			progress, err := env.store.GradingProgress(env.sessA)
			if err != nil || calls.Load() != 3 || progress[env.threadA].Attempts != 3 {
				t.Fatalf("provider calls %d, progress %+v, %v", calls.Load(), progress, err)
			}
			if persistent {
				score, err := env.store.GetScore(env.threadA)
				if err != nil || score != nil {
					t.Fatalf("published failed score: %+v, %v", score, err)
				}
				if grade, err := env.store.GetGrade(env.sessA); err != nil || grade != nil {
					t.Fatalf("published failed grade: %+v, %v", grade, err)
				}
				if p := progress[env.threadA]; p.State != "failed" || p.LastError != "HTTP 503" {
					t.Fatalf("diagnostics: %+v", p)
				}
				review := fmt.Sprintf("/review/%d", env.sessA)
				for _, role := range []string{"teacher", "admin"} {
					body := env.do(role, http.MethodGet, review, nil).Body.String()
					if !strings.Contains(body, "Retry grading") || !strings.Contains(body, "HTTP 503") || strings.Contains(body, "private provider detail") {
						t.Fatalf("diagnostics for %s: %s", role, body)
					}
				}
				studentBody := env.do("studentA", http.MethodGet, results, nil).Body.String()
				if !strings.Contains(studentBody, "Your answers are saved") || strings.Contains(studentBody, "HTTP 503") || strings.Contains(studentBody, "retry-grading") {
					t.Fatal("student failure view")
				}
				if rec := env.do("studentA", http.MethodPost, review+"/retry-grading", nil); rec.Code != 403 {
					t.Fatalf("student retry: %d", rec.Code)
				}
				if rec := env.do("admin", http.MethodPost, review+"/retry-grading", nil); rec.Code != 303 {
					t.Fatalf("admin retry: %d", rec.Code)
				}
				if rec := env.do("teacher", http.MethodPost, review+"/retry-grading", nil); rec.Code != 409 {
					t.Fatalf("duplicate staff retry: %d", rec.Code)
				}
				waitForState(t, func() bool {
					s, err := env.store.GetSession(env.sessA)
					return err == nil && s.Status == model.StatusGraded
				})
				if calls.Load() != 4 {
					t.Fatalf("manual retry repeated %d calls", calls.Load()-3)
				}
			}
			if grade, err := env.store.GetGrade(env.sessA); err != nil || grade == nil || grade.LLMGrade != 70 {
				t.Fatalf("grade: %+v, %v", grade, err)
			}
		})
	}
}

// Drop an in-flight worker's result between dispatch and the store commit.
// A replacement must enforce the HTTP budget even if the first call succeeded.
func TestDispatchBudgetAcrossWorkerRestart(t *testing.T) {
	for _, sent := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_worker_sent=%t", sent), func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if !sent && n < 3 {
					w.WriteHeader(503)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"score\":7,\"max_points\":10}"}}]}`)
			}))
			t.Cleanup(provider.Close)
			retries := 2
			if sent {
				retries = 0
			}
			client, err := llm.New(provider.URL, "test", "fake", "standard", llm.WithMaxRetries(retries))
			if err != nil {
				t.Fatal(err)
			}
			env := newAnswerEnv(t, 0, client)
			if _, err := env.store.AddMessage(model.Message{ThreadID: env.threadID, Role: model.RoleStudent, Content: "answer"}); err != nil {
				t.Fatal(err)
			}
			if rec := env.submit(); rec.Code != 303 {
				t.Fatal(rec.Code)
			}
			lost, err := env.store.ClaimEvaluation(false)
			if err != nil || lost == nil {
				t.Fatalf("first claim: %+v, %v", lost, err)
			}
			if sent {
				_, score, err := env.h.evaluateJob(t.Context(), *lost)
				if err != nil || score == nil || calls.Load() != 1 {
					t.Fatalf("first dispatch: %+v, %v, calls %d", score, err, calls.Load())
				}
				// The response never reached FinishGrading.
				if err := env.store.ReleaseEvaluation(*lost); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, stop := context.WithCancel(t.Context())
				stop()
				env.h.processEvaluation(ctx, *lost)
				if calls.Load() != 0 {
					t.Fatal("cancelled claim reached upstream")
				}
			}
			t.Cleanup(env.h.StartWorkers(t.Context(), 1))
			want := model.StatusGraded
			if sent {
				want = model.StatusGradingFailed
			}
			waitForState(t, func() bool { s, err := env.store.GetSession(env.sessID); return err == nil && s.Status == want })
			progress, err := env.store.GradingProgress(env.sessID)
			p := progress[env.threadID]
			if err != nil || p.Attempts != retries+1 || int(calls.Load()) != retries+1 {
				t.Fatalf("progress %+v, HTTP requests %d, %v", p, calls.Load(), err)
			}
			if sent && p.LastError != "attempts_exhausted" {
				t.Fatalf("exhausted budget: %+v", p)
			}
		})
	}
}
