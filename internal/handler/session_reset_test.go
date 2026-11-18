package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func TestResetAttemptRoles(t *testing.T) {
	for _, role := range []string{"anon", "studentA", "studentB", "teacher", "admin"} {
		t.Run(role, func(t *testing.T) {
			env := newRBACEnv(t)
			path := fmt.Sprintf("/review/%d/reset", env.sessA)
			want := http.StatusForbidden
			switch role {
			case "anon":
				want = http.StatusSeeOther
			case "teacher", "admin":
				want = http.StatusOK
			}
			if rec := env.do(role, http.MethodGet, path, nil); rec.Code != want {
				t.Fatalf("GET: %d, %s", rec.Code, rec.Body.String())
			}
			if want == http.StatusOK {
				want = http.StatusSeeOther
			}
			rec := env.do(role, http.MethodPost, path, url.Values{"confirm": {"yes"}})
			if rec.Code != want {
				t.Fatalf("POST: %d, %s", rec.Code, rec.Body.String())
			}
			sess, err := env.store.GetSession(env.sessA)
			if err != nil {
				t.Fatal(err)
			}
			if (sess.Status == model.StatusAnnulled) != (role == "teacher" || role == "admin") {
				t.Fatalf("unexpected status: %s", sess.Status)
			}
		})
	}
}

func TestResetAttemptValidationAndCSRF(t *testing.T) {
	env := newRBACEnv(t)
	path := fmt.Sprintf("/review/%d/reset", env.sessA)
	for name, form := range map[string]url.Values{
		"confirmation missing":     {"reason": {"Keep this reason"}},
		"confirmation in URL only": {},
		"reason too long":          {"confirm": {"yes"}, "reason": {strings.Repeat("я", model.MaxResetReasonRunes+1)}},
	} {
		t.Run(name, func(t *testing.T) {
			target := path
			if name == "confirmation in URL only" {
				target += "?confirm=yes"
			}
			rec := env.do("teacher", http.MethodPost, target, form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("validation: %d, %s", rec.Code, rec.Body.String())
			}
			if name == "confirmation missing" && !strings.Contains(rec.Body.String(), "Keep this reason") {
				t.Fatal("reason lost on validation error")
			}
		})
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("confirm=yes&csrf_token=wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: env.tokens["teacher"]})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfTestToken})
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("CSRF: %d", rec.Code)
	}
	sess, err := env.store.GetSession(env.sessA)
	if err != nil || sess.Status != model.StatusInProgress || sess.ResetAt != nil {
		t.Fatalf("invalid request changed attempt: %+v, %v", sess, err)
	}
	for path, want := range map[string]int{"/review/0/reset": 400, "/review/no/reset": 400, "/review/999999/reset": 404} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if rec := env.do("teacher", method, path, url.Values{"confirm": {"yes"}}); rec.Code != want {
				t.Fatalf("%s %s: %d, want %d", method, path, rec.Code, want)
			}
		}
	}
}

func TestResetAttemptHistoryIsReadOnly(t *testing.T) {
	env := newRBACEnv(t)
	if _, err := env.store.AddMessage(model.Message{ThreadID: env.threadGraded, Role: model.RoleStudent, Content: "Preserved answer"}); err != nil {
		t.Fatal(err)
	}
	reason := "Network failure <script>alert(1)</script>"
	path := fmt.Sprintf("/review/%d", env.sessGraded)
	rec := env.do("teacher", http.MethodPost, path+"/reset", url.Values{"confirm": {"yes"}, "reason": {reason}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
		t.Fatalf("reset: %d, %s", rec.Code, rec.Body.String())
	}
	before, err := env.store.GetSessionView(env.sessGraded)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ role, path string }{
		{"teacher", path}, {"teacher", path + "/reset"},
		{"studentA", fmt.Sprintf("/exam/%d", env.sessGraded)},
		{"studentA", fmt.Sprintf("/results/%d", env.sessGraded)},
	} {
		rec := env.do(target.role, http.MethodGet, target.path, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "Attempt reset") || !strings.Contains(body, "Network failure &lt;script&gt;") || !strings.Contains(body, "teacher (user #") {
			t.Fatalf("missing history/audit on %s: %d, %s", target.path, rec.Code, body)
		}
		for _, form := range []string{"name=\"teacher_score\"", "name=\"final_grade\"", "name=\"answer\"", "name=\"confirm\""} {
			if strings.Contains(body, form) {
				t.Fatalf("reset history has editable form %s on %s", form, target.path)
			}
		}
		if target.path != path+"/reset" && !strings.Contains(body, "Preserved answer") {
			t.Fatalf("answer missing on %s", target.path)
		}
	}
	for _, target := range []struct {
		role, path string
		form       url.Values
	}{
		{"teacher", path + "/reset", url.Values{"confirm": {"yes"}, "reason": {"Overwrite"}}},
		{"teacher", fmt.Sprintf("%s/score/%d", path, env.threadGraded), url.Values{"teacher_score": {"1"}}},
		{"teacher", path + "/finalize", url.Values{"final_grade": {"10"}}},
		{"studentA", fmt.Sprintf("/exam/%d/answer/%d", env.sessGraded, env.threadGraded), url.Values{"answer": {"Late answer"}}},
		{"studentA", fmt.Sprintf("/exam/%d/submit", env.sessGraded), nil},
	} {
		if rec := env.do(target.role, http.MethodPost, target.path, target.form); rec.Code != http.StatusConflict {
			t.Fatalf("late write %s: %d, %s", target.path, rec.Code, rec.Body.String())
		}
	}
	after, err := env.store.GetSessionView(env.sessGraded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reset history changed")
	}
	for _, target := range []struct{ role, path string }{{"studentA", "/"}, {"teacher", "/review"}} {
		rec := env.do(target.role, http.MethodGet, target.path, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Attempt reset") {
			t.Fatalf("reset label missing on %s", target.path)
		}
	}
}

func TestResetPermitsFreshDrawFromCurrentBank(t *testing.T) {
	s := newExamStore(t)
	examStore(t, s, 1, 0, 0)
	actor, err := s.CreateUser(model.User{Username: "teacher", Role: model.UserRoleTeacher, Active: true, PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{model.DifficultyEasy: 1}, Shuffle: true}}
	user := student(77)
	oldID := startedSession(t, h, user, "t")
	oldQuestions := sessionQuestions(t, s, oldID)
	q := oldQuestions[0]
	q.Published = false
	if err := s.InQuestionTx(t.Context(), func(tx *store.QuestionTx) error { return tx.Update(q) }); err != nil {
		t.Fatal(err)
	}
	replacement := insertExamQuestion(t, s, examQuestion("Replacement question", "t"))
	if err := s.ResetSession(oldID, actor, ""); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.handleIndex(rec, withExamUser(httptest.NewRequest(http.MethodGet, "/", nil), user))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "action=\"/exam/start\"") || strings.Contains(rec.Body.String(), fmt.Sprintf("href=\"/exam/%d\"", oldID)) {
		t.Fatal("home page does not offer a fresh attempt")
	}
	fresh := startedSession(t, h, user, "t")
	qs := sessionQuestions(t, s, fresh)
	if fresh == oldID || len(qs) != 1 || qs[0].ID != replacement {
		t.Fatalf("old variant resumed: %d, %+v", fresh, qs)
	}
	preserved := sessionQuestions(t, s, oldID)
	if len(preserved) != 1 || preserved[0].ID != q.ID {
		t.Fatal("old variant lost")
	}
}

func TestResetDuringLLMWork(t *testing.T) {
	for _, mode := range []string{"answer", "answer error", "grading", "grading error", "grading queue timeout"} {
		t.Run(mode, func(t *testing.T) {
			env := newRBACEnv(t)
			legacy, err := env.store.GetSession(env.sessA)
			if err != nil {
				t.Fatal(err)
			}
			first, err := env.store.GetThread(env.threadA)
			if err != nil {
				t.Fatal(err)
			}
			second := insertExamQuestion(t, env.store, examQuestion("Second question", "t"))
			active, _, err := env.store.StartSession(legacy.BlueprintID, legacy.StudentID, "t", []int64{first.QuestionID, second}, false)
			if err != nil {
				t.Fatal(err)
			}
			threads, err := env.store.GetThreadsForSession(active.ID)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int32
			block := func(context.Context) error {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				if mode == "grading queue timeout" {
					return llm.ErrQueueTimeout
				}
				if strings.HasSuffix(mode, "error") {
					return fmt.Errorf("provider failed")
				}
				return nil
			}
			env.h.llm = contextEvaluator{evaluate: block, grade: block}
			path := fmt.Sprintf("/exam/%d/answer/%d", active.ID, threads[0].ID)
			form := url.Values{"answer": {"Saved answer"}, "answer_key": {"0"}}
			if !strings.HasPrefix(mode, "answer") {
				path = fmt.Sprintf("/exam/%d/submit", active.ID)
				form = nil
				for _, thread := range threads {
					if _, err := env.store.AddMessage(model.Message{ThreadID: thread.ID, Role: model.RoleStudent, Content: "Saved answer"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			rec := env.do("studentA", http.MethodPost, path, form)
			if rec.Code != http.StatusAccepted && rec.Code != http.StatusSeeOther {
				t.Fatalf("queue work: %d, %s", rec.Code, rec.Body.String())
			}
			job, err := env.store.ClaimEvaluation(false)
			if err != nil || job == nil {
				t.Fatalf("claim work: %+v, %v", job, err)
			}
			done := make(chan struct{})
			go func() { env.h.processEvaluation(t.Context(), *job); close(done) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("LLM was not called")
			}
			reset := env.do("teacher", http.MethodPost, fmt.Sprintf("/review/%d/reset", active.ID), url.Values{"confirm": {"yes"}})
			if reset.Code != http.StatusSeeOther {
				t.Fatalf("reset during LLM: %d, %s", reset.Code, reset.Body.String())
			}
			before, err := env.store.GetSessionView(active.ID)
			if err != nil {
				t.Fatal(err)
			}
			fresh := env.do("studentA", http.MethodPost, "/exam/start", url.Values{"topic": {"t"}})
			if fresh.Code != http.StatusSeeOther || fresh.Header().Get("Location") == fmt.Sprintf("/exam/%d", active.ID) {
				t.Fatalf("cannot start while old LLM runs: %d, %s", fresh.Code, fresh.Body.String())
			}
			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("late request did not finish")
			}
			if next, err := env.store.ClaimEvaluation(false); err != nil || next != nil {
				t.Fatalf("reset left pending work: %+v, %v", next, err)
			}
			after, err := env.store.GetSessionView(active.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || calls.Load() != 1 {
				t.Fatalf("late work changed history or continued grading: %d calls", calls.Load())
			}
		})
	}
}

func TestResetAttemptRussianBasePath(t *testing.T) {
	env := newRBACEnv(t)
	env.h.config.BasePath = "/ru"
	router := chi.NewRouter()
	router.Use(i18n.Middleware("ru"))
	router.Route("/ru", func(sub chi.Router) {
		sub.Use(env.h.BasePathMiddleware)
		env.h.Routes(sub)
	})
	env.router = router
	path := fmt.Sprintf("/ru/review/%d/reset", env.sessA)
	rec := env.do("teacher", http.MethodGet, path, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Сбросить попытку") || !strings.Contains(rec.Body.String(), "action=\""+path+"\"") {
		t.Fatalf("Russian reset form: %d, %s", rec.Code, rec.Body.String())
	}
	rec = env.do("teacher", http.MethodPost, path, url.Values{"confirm": {"yes"}, "reason": {"Техническая ошибка"}})
	target := fmt.Sprintf("/ru/review/%d", env.sessA)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != target {
		t.Fatalf("base redirect: %d, %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = env.do("teacher", http.MethodGet, target, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Попытка аннулирована") || !strings.Contains(rec.Body.String(), "Техническая ошибка") {
		t.Fatalf("Russian history: %d, %s", rec.Code, rec.Body.String())
	}
}
