package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
// whatever the limit says. It counts evaluation calls.
type fakeLLM struct {
	mu    sync.Mutex
	calls int

	emptyFollowupQ bool  // ask for a follow-up but leave the question empty
	err            error // returned by EvaluateAnswer, if non-nil
}

func (f *fakeLLM) EvaluateAnswer(_ context.Context, q model.Question, _ []model.Message, _ int, _, _ int64) (*llm.GradeResult, string, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if f.err != nil {
		return nil, "", f.err
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
		Topic: "t", Rubric: "r", ModelAnswer: "a", MaxPoints: 10, Published: true,
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
	r.Get("/exam/{sessionID}/answer/{threadID}", h.handleFeedback)
	r.Get("/exam/{sessionID}", h.handleExamPage)
	r.Post("/exam/{sessionID}/submit", h.handleSubmit)
	r.Get("/results/{sessionID}", h.handleStudentResults)

	return &answerEnv{h: h, store: s, router: r, sessID: sessID, threadID: threads[0].ID}
}

func (e *answerEnv) answerPath() string {
	return fmt.Sprintf("/exam/%d/answer/%d", e.sessID, e.threadID)
}

func (e *answerEnv) turn(t *testing.T) string {
	t.Helper()
	messages, err := e.store.GetMessages(e.threadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		return "0"
	}
	return strconv.FormatInt(messages[len(messages)-1].ID, 10)
}

func (e *answerEnv) post(t *testing.T, answer string) *httptest.ResponseRecorder {
	t.Helper()
	return e.postForm(url.Values{"answer": {answer}, "answer_key": {e.turn(t)}})
}

func (e *answerEnv) postForm(form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, e.answerPath(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func runNextEvaluation(t *testing.T, h *Handler) {
	t.Helper()
	job, err := h.store.ClaimEvaluation(false)
	if err != nil || job == nil {
		t.Fatalf("expected a queued job, got %+v, %v", job, err)
	}
	h.processEvaluation(t.Context(), *job)
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
		rec := env.post(t, fmt.Sprintf("answer %d", i))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("answer %d: status %d, body %q", i, rec.Code, rec.Body.String())
		}
		runNextEvaluation(t, env.h)
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

	if rec := env.post(t, "answer"); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	runNextEvaluation(t, env.h)
	th := env.thread(t)
	if th.Status != model.ThreadCompleted || th.FollowupCount != 0 {
		t.Errorf("thread = %+v, want completed with 0 follow-ups", th)
	}
}

func TestAnswerWithEmptyFollowupQuestionCompletesThread(t *testing.T) {
	env := newAnswerEnv(t, 3, &fakeLLM{emptyFollowupQ: true})

	if rec := env.post(t, "answer"); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	runNextEvaluation(t, env.h)
	th := env.thread(t)
	if th.Status != model.ThreadCompleted || th.FollowupCount != 0 {
		t.Errorf("thread = %+v, want completed with 0 follow-ups (no question was asked)", th)
	}
}

func TestAnswerToCompletedThreadIsRejected(t *testing.T) {
	fake := &fakeLLM{}
	env := newAnswerEnv(t, 0, fake)
	env.post(t, "first")
	runNextEvaluation(t, env.h)
	before := env.messageCount(t)
	calls := fake.callCount()

	rec := env.post(t, "sneaky extra answer")

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

func TestConcurrentAndStaleFormsCannotDuplicateAnswers(t *testing.T) {
	fake := &fakeLLM{}
	env := newAnswerEnv(t, 3, fake)
	form := url.Values{"answer": {"same answer"}, "answer_key": {"0"}}
	ready := make(chan struct{})
	done := make(chan int, 2)
	for range 2 {
		go func() {
			<-ready
			done <- env.postForm(form).Code
		}()
	}
	close(ready)
	codes := map[int]int{}
	for range 2 {
		codes[<-done]++
	}
	if codes[http.StatusAccepted] != 1 || codes[http.StatusConflict] != 1 || env.messageCount(t) != 1 {
		t.Fatalf("concurrent POSTs: status counts %v, messages %d", codes, env.messageCount(t))
	}
	runNextEvaluation(t, env.h)
	if rec := env.postForm(form); rec.Code != http.StatusConflict || rec.Header().Get("X-Feedback-Conflict") == "" || !strings.Contains(rec.Body.String(), "follow-up 1?") {
		t.Fatalf("stale tab did not get the current thread: %d, %s", rec.Code, rec.Body.String())
	}
	if env.messageCount(t) != 2 || fake.callCount() != 1 {
		t.Fatal("a replay duplicated the answer or feedback")
	}
	// The same text is valid for a genuinely new follow-up turn.
	if rec := env.post(t, "same answer"); rec.Code != http.StatusAccepted {
		t.Fatalf("new turn: %d", rec.Code)
	}
}

func TestUnavailableFeedbackRetriesOnlyTheSavedAnswer(t *testing.T) {
	const detail = "provider connection refused"
	fake := &fakeLLM{err: errors.New(detail)}
	env := newAnswerEnv(t, 3, fake)
	if rec := env.post(t, "my answer"); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "Answer saved.") {
		t.Fatalf("acknowledgement: %d, %s", rec.Code, rec.Body.String())
	}
	runNextEvaluation(t, env.h)
	page := env.get(t, env.answerPath())
	if !strings.Contains(page, "my answer") || !strings.Contains(page, "Your answer is saved") || !strings.Contains(page, "Retry evaluation") || strings.Contains(page, detail) {
		t.Fatalf("failed feedback: %s", page)
	}
	if env.thread(t).Status != model.ThreadFeedbackFailed || env.messageCount(t) != 1 {
		t.Fatal("the failed evaluation lost or duplicated the saved answer")
	}
	if rec := env.post(t, "changed answer"); rec.Code != http.StatusConflict {
		t.Fatalf("overwriting a saved answer: %d", rec.Code)
	}
	fake.err = nil
	form := url.Values{"answer_key": {env.turn(t)}, "retry": {"1"}, "answer": {"untrusted replacement"}}
	if rec := env.postForm(form); rec.Code != http.StatusAccepted {
		t.Fatalf("retry: %d", rec.Code)
	}
	if rec := env.postForm(form); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate retry: %d", rec.Code)
	}
	runNextEvaluation(t, env.h)
	messages, err := env.store.GetMessages(env.threadID)
	if err != nil || len(messages) != 2 || messages[0].Content != "my answer" || messages[1].Role != model.RoleLLM || fake.callCount() != 2 {
		t.Fatalf("retried conversation: %+v, %v", messages, err)
	}
}

// A pre-queue database can have a saved answer with neither a job nor feedback.
func TestNativeFormResumesLegacySavedAnswer(t *testing.T) {
	env := newAnswerEnv(t, 0, &fakeLLM{})
	id, err := env.store.AddMessage(model.Message{ThreadID: env.threadID, Role: model.RoleStudent, Content: "legacy answer"})
	if err != nil {
		t.Fatal(err)
	}
	page := env.get(t, fmt.Sprintf("/exam/%d", env.sessID))
	key := strconv.FormatInt(id, 10)
	if !strings.Contains(page, "Retry evaluation") || !strings.Contains(page, `name="answer_key" value="`+key+`"`) {
		t.Fatal("the legacy answer does not expose a retry form for its saved turn")
	}
	form := url.Values{"answer_key": {key}, "retry": {"1"}}
	req := httptest.NewRequest(http.MethodPost, env.answerPath(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/exam/%d#thread-%d", env.sessID, env.threadID) {
		t.Fatalf("native retry: %d, %s", rec.Code, rec.Header().Get("Location"))
	}
	runNextEvaluation(t, env.h)
	if env.thread(t).Status != model.ThreadCompleted || env.messageCount(t) != 2 {
		t.Fatal("legacy retry lost or duplicated the answer")
	}
}

func TestEvaluatorMarkdownRenderedStudentTextNot(t *testing.T) {
	env := newAnswerEnv(t, 3, &fakeLLM{})
	if rec := env.post(t, "I think **this** <script>alert(1)</script>"); rec.Code != http.StatusAccepted {
		t.Fatalf("status %d", rec.Code)
	}
	runNextEvaluation(t, env.h)
	page := env.get(t, fmt.Sprintf("/exam/%d", env.sessID))

	// The fake LLM asks a follow-up, which is joined with model.FollowupMarker.
	if !strings.Contains(page, "<strong>Follow-up question:</strong>") {
		t.Error("evaluator follow-up marker should render as bold")
	}
	if strings.Contains(page, "**Follow-up question:**") {
		t.Error("raw Markdown asterisks should not be shown")
	}
	// The student's answer is plain text: asterisks stay, HTML is escaped.
	if !strings.Contains(page, "I think **this**") {
		t.Error("student answer should be shown verbatim")
	}
	if strings.Contains(page, "<script>alert(1)") {
		t.Error("student answer HTML must be escaped")
	}
}
