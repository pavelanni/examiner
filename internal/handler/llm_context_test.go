package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
)

// contextEvaluator наблюдает контекст на границе обработчика и клиента LLM.
type contextEvaluator struct {
	evaluate       func(context.Context) error
	grade          func(context.Context) error
	gradedMessages chan<- []model.Message
}

func (e contextEvaluator) EvaluateAnswer(ctx context.Context, _ model.Question, _ []model.Message, _ int, _, _ int64) (*llm.GradeResult, string, error) {
	if err := e.evaluate(ctx); err != nil {
		return nil, "", err
	}
	return &llm.GradeResult{Score: 5, MaxPoints: 10}, "", nil
}

func (e contextEvaluator) GradeThreadAttempt(ctx context.Context, _ model.Question, messages []model.Message, _, _ int64, _ llm.GradeAttempt) (*llm.GradeResult, error) {
	if e.gradedMessages != nil {
		e.gradedMessages <- messages
	}
	if err := e.grade(ctx); err != nil {
		return nil, err
	}
	return &llm.GradeResult{Score: 5, MaxPoints: 10}, nil
}

// Block the provider for up to a minute, with an explicit release so a passing
// test can verify completion without waiting for the artificial delay.
func slowFeedback(entered, release chan struct{}) func(context.Context) error {
	return func(ctx context.Context) error {
		close(entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(60 * time.Second):
		case <-release:
		}
		return nil
	}
}

func waitForState(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !check() {
		select {
		case <-deadline.C:
			t.Fatal("worker did not reach expected state")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestSlowFeedbackAcknowledgesWithinOneSecondAndSurvivesDisconnect(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	env := newAnswerEnv(t, 3, contextEvaluator{evaluate: slowFeedback(entered, release)})
	t.Cleanup(env.h.StartWorkers(t.Context(), 1))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	form := url.Values{"answer": {"saved answer"}, "answer_key": {"0"}}
	req := httptest.NewRequest(http.MethodPost, env.answerPath(), strings.NewReader(form.Encode())).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { env.router.ServeHTTP(rec, req); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("HTTP POST waited for the slow provider")
	}
	cancel()
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "Answer saved.") || !strings.Contains(rec.Body.String(), "saved answer") {
		t.Fatalf("acknowledgement: %d, %s", rec.Code, rec.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("saved answer did not reach the provider")
	}
	if env.messageCount(t) != 1 || env.thread(t).Status != model.ThreadEvaluating {
		t.Fatal("waiting answer was not saved")
	}
	poll := httptest.NewRequest(http.MethodGet, env.answerPath(), nil)
	poll.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, poll)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("pending poll: %d, %s", rec.Code, rec.Body.String())
	}
	close(release)
	waitForState(t, func() bool { return env.thread(t).Status == model.ThreadCompleted })
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, poll)
	if rec.Code != http.StatusOK || env.messageCount(t) != 2 || strings.Contains(rec.Body.String(), "every 2s") {
		t.Fatalf("finished poll: %d, %s", rec.Code, rec.Body.String())
	}
}

func TestShutdownReturnsInFlightFeedbackToQueue(t *testing.T) {
	entered := make(chan struct{})
	env := newAnswerEnv(t, 0, contextEvaluator{evaluate: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}})
	stop := env.h.StartWorkers(t.Context(), 1)
	t.Cleanup(stop)
	if rec := env.post(t, "saved through restart"); rec.Code != http.StatusAccepted {
		t.Fatalf("answer: %d", rec.Code)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("feedback worker did not start")
	}
	stop()
	if env.thread(t).Status != model.ThreadEvaluating || env.messageCount(t) != 1 {
		t.Fatal("stopping the worker changed or lost the pending answer")
	}
	env.h.llm = &fakeLLM{}
	runNextEvaluation(t, env.h)
	if env.thread(t).Status != model.ThreadCompleted || env.messageCount(t) != 2 {
		t.Fatal("a new worker could not finish the saved answer")
	}
}

func (e *answerEnv) submit() *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/exam/%d/submit", e.sessID), nil))
	return rec
}

func TestSubmitGradesSavedAnswerWhileFeedbackIsRunning(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	graded := make(chan []model.Message, 1)
	env := newAnswerEnv(t, 3, contextEvaluator{
		evaluate:       slowFeedback(entered, release),
		grade:          func(ctx context.Context) error { return ctx.Err() },
		gradedMessages: graded,
	})
	if rec := env.post(t, "saved answer"); rec.Code != http.StatusAccepted {
		t.Fatalf("answer: %d", rec.Code)
	}
	feedback, err := env.store.ClaimEvaluation(true)
	if err != nil || feedback == nil {
		t.Fatalf("claim feedback: %+v, %v", feedback, err)
	}
	done := make(chan struct{})
	go func() {
		env.h.processEvaluation(t.Context(), *feedback)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("feedback has not started")
	}
	began := time.Now()
	if rec := env.submit(); rec.Code != http.StatusSeeOther || time.Since(began) >= time.Second {
		t.Fatalf("submit while feedback pending: %d after %s", rec.Code, time.Since(began))
	}
	runNextEvaluation(t, env.h)
	select {
	case messages := <-graded:
		if len(messages) != 1 || messages[0].Content != "saved answer" || messages[0].Role != model.RoleStudent {
			t.Fatalf("grading input: %+v", messages)
		}
	case <-time.After(time.Second):
		t.Fatal("final grading waited for in-flight feedback")
	}
	sess, err := env.store.GetSession(env.sessID)
	if err != nil || sess.Status != model.StatusGraded {
		t.Fatalf("final grading while feedback is pending: %+v, %v", sess, err)
	}

	// Allow the late result to reach FinishFeedback with a live context.
	// Cancelling its worker here would hide a stale write to the submitted exam.
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("late feedback did not finish")
	}
	if env.messageCount(t) != 1 || env.thread(t).Status != model.ThreadCompleted {
		t.Fatal("late feedback changed the submitted conversation")
	}
}

// TestQueueTimeoutKeepsExamRetryable не превращает нехватку ёмкости в нулевую оценку.
func TestQueueTimeoutKeepsExamRetryable(t *testing.T) {
	calls := 0
	env := newAnswerEnv(t, 3, contextEvaluator{grade: func(context.Context) error {
		calls++
		if calls == 1 {
			return fmt.Errorf("%w: %w", llm.ErrQueueTimeout, context.DeadlineExceeded)
		}
		return nil
	}})
	if _, err := env.store.AddMessage(model.Message{ThreadID: env.threadID, Role: model.RoleStudent, Content: "answer"}); err != nil {
		t.Fatal(err)
	}
	if rec := env.submit(); rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d, body=%s", rec.Code, rec.Body.String())
	}
	runNextEvaluation(t, env.h)
	sess, err := env.store.GetSession(env.sessID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != model.StatusGradingFailed {
		t.Fatalf("grading failure reopened the exam: %s", sess.Status)
	}
	score, err := env.store.GetScore(env.threadID)
	if err != nil {
		t.Fatal(err)
	}
	if score != nil {
		t.Fatalf("capacity timeout recorded a score: %+v", score)
	}
	grade, err := env.store.GetGrade(env.sessID)
	if err != nil {
		t.Fatal(err)
	}
	if grade != nil {
		t.Fatalf("capacity timeout finalized the exam: %+v", grade)
	}
	if rec := env.submit(); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate submit: status=%d", rec.Code)
	}
	if err := env.store.RetryGrading(env.sessID); err != nil {
		t.Fatal(err)
	}
	runNextEvaluation(t, env.h)
	sess, err = env.store.GetSession(env.sessID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != model.StatusGraded {
		t.Fatalf("retry did not complete: %s", sess.Status)
	}
	score, err = env.store.GetScore(env.threadID)
	if err != nil {
		t.Fatal(err)
	}
	if score == nil || score.LLMScore != 5 {
		t.Fatalf("retry score=%+v, want 5", score)
	}
}
