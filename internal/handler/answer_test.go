package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// fakeLLM is an Evaluator that always asks for another follow-up,
// whatever the limit says. It counts calls and can block to hold a request in flight.
type fakeLLM struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{} // receives once per EvaluateAnswer call, if non-nil
	release chan struct{} // EvaluateAnswer blocks until it is closed, if non-nil

	emptyFollowupQ bool // ask for a follow-up but leave the question empty
}

func (f *fakeLLM) EvaluateAnswer(_ context.Context, q model.Question, _ []model.Message, _ int, _, _ int64) (*llm.GradeResult, string, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	res := &llm.GradeResult{
		Score:        1,
		MaxPoints:    q.MaxPoints,
		Feedback:     fmt.Sprintf("feedback %d", n),
		NeedFollowup: true,
		FollowupQ:    fmt.Sprintf("follow-up %d?", n),
	}
	if f.emptyFollowupQ {
		res.FollowupQ = ""
	}
	return res, "", nil
}

func (f *fakeLLM) GradeThread(context.Context, model.Question, []model.Message, int64, int64) (*llm.GradeResult, error) {
	return &llm.GradeResult{}, nil
}

func (f *fakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type answerEnv struct {
	h        *Handler
	store    *store.Store
	router   http.Handler
	sessID   int64
	threadID int64
}

func newAnswerEnv(t *testing.T, maxFollowups int, ev Evaluator) *answerEnv {
	t.Helper()
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bpID, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "T", MaxFollowups: maxFollowups})
	if err != nil {
		t.Fatalf("CreateBlueprint: %v", err)
	}
	qID, err := s.InsertQuestion(model.Question{
		CourseID: 1, Text: "Q1", Difficulty: model.Difficulty("easy"),
		Topic: "t", Rubric: "r", ModelAnswer: "a", MaxPoints: 10,
	})
	if err != nil {
		t.Fatalf("InsertQuestion: %v", err)
	}
	sessID, err := s.CreateSession(bpID, 1, []int64{qID})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	threads, err := s.GetThreadsForSession(sessID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("GetThreadsForSession: %v (%d threads)", err, len(threads))
	}

	h := &Handler{store: s, llm: ev}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			u := &model.User{ID: 1, Role: model.UserRoleStudent}
			ctx := i18n.WithLocalizer(model.ContextWithUser(req.Context(), u), i18n.NewLocalizer("en"))
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Post("/exam/{sessionID}/answer/{threadID}", h.handleAnswer)

	return &answerEnv{h: h, store: s, router: r, sessID: sessID, threadID: threads[0].ID}
}

func (e *answerEnv) post(answer string) *httptest.ResponseRecorder {
	form := url.Values{"answer": {answer}}
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/exam/%d/answer/%d", e.sessID, e.threadID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *answerEnv) thread(t *testing.T) model.QuestionThread {
	t.Helper()
	th, err := e.store.GetThread(e.threadID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	return th
}

func (e *answerEnv) messageCount(t *testing.T) int {
	t.Helper()
	msgs, err := e.store.GetMessages(e.threadID)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	return len(msgs)
}

func TestAnswerEnforcesFollowupLimit(t *testing.T) {
	const limit = 3
	env := newAnswerEnv(t, limit, &fakeLLM{})

	for i := 1; i <= limit+1; i++ {
		rec := env.post(fmt.Sprintf("answer %d", i))
		if rec.Code != http.StatusOK {
			t.Fatalf("answer %d: status %d, body %q", i, rec.Code, rec.Body.String())
		}
		th := env.thread(t)
		if th.FollowupCount > limit {
			t.Fatalf("after answer %d: FollowupCount = %d, exceeds limit %d", i, th.FollowupCount, limit)
		}
		if i <= limit && th.Status != model.ThreadAnswered {
			t.Fatalf("after answer %d: status %q, want answered", i, th.Status)
		}
	}

	th := env.thread(t)
	if th.FollowupCount != limit {
		t.Errorf("FollowupCount = %d, want exactly %d", th.FollowupCount, limit)
	}
	if th.Status != model.ThreadCompleted {
		t.Errorf("status = %q, want completed once the limit is reached", th.Status)
	}
	msgs, _ := env.store.GetMessages(env.threadID)
	last := msgs[len(msgs)-1]
	if last.Role != model.RoleLLM || last.Followup != "" {
		t.Errorf("last message should be LLM feedback without follow-up, got %+v", last)
	}
}

func TestAnswerWithZeroFollowupsCompletesImmediately(t *testing.T) {
	env := newAnswerEnv(t, 0, &fakeLLM{})

	if rec := env.post("answer"); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	th := env.thread(t)
	if th.Status != model.ThreadCompleted || th.FollowupCount != 0 {
		t.Errorf("thread = %+v, want completed with 0 follow-ups", th)
	}
}

func TestAnswerWithEmptyFollowupQuestionCompletesThread(t *testing.T) {
	env := newAnswerEnv(t, 3, &fakeLLM{emptyFollowupQ: true})

	if rec := env.post("answer"); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	th := env.thread(t)
	if th.Status != model.ThreadCompleted || th.FollowupCount != 0 {
		t.Errorf("thread = %+v, want completed with 0 follow-ups (no question was asked)", th)
	}
}

func TestAnswerToCompletedThreadIsRejected(t *testing.T) {
	fake := &fakeLLM{}
	env := newAnswerEnv(t, 0, fake)
	env.post("first") // max-followups 0: the thread completes at once
	before := env.messageCount(t)
	calls := fake.callCount()

	rec := env.post("sneaky extra answer")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if got := env.messageCount(t); got != before {
		t.Errorf("messages = %d, want %d (nothing added)", got, before)
	}
	if fake.callCount() != calls {
		t.Error("LLM must not be called for a completed thread")
	}
}

func TestConcurrentAnswersToSameThreadAreRejected(t *testing.T) {
	fake := &fakeLLM{entered: make(chan struct{}, 2), release: make(chan struct{})}
	env := newAnswerEnv(t, 3, fake)

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- env.post("first") }()
	<-fake.entered // first request is now inside the LLM call

	rec := env.post("double click")
	if rec.Code != http.StatusConflict {
		t.Errorf("second concurrent answer: status = %d, want 409", rec.Code)
	}

	close(fake.release)
	if rec := <-first; rec.Code != http.StatusOK {
		t.Errorf("first answer: status = %d, want 200", rec.Code)
	}
	if fake.callCount() != 1 {
		t.Errorf("LLM calls = %d, want 1", fake.callCount())
	}
	if got := env.messageCount(t); got != 2 {
		t.Errorf("messages = %d, want 2 (one student, one LLM)", got)
	}
}
