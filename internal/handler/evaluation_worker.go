package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// StartWorkers starts the exam server's shared dispatcher after startup recovery.
// The caller stops it before closing the store. No job belongs to an HTTP request.
func (h *Handler) StartWorkers(parent context.Context, concurrency int) func() {
	ctx, cancel := context.WithCancel(parent)
	h.workReady = make(chan struct{}, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		h.dispatch(ctx, max(1, concurrency))
	}()
	return func() { cancel(); <-stopped }
}

func (h *Handler) wakeEvaluation() {
	select {
	case h.workReady <- struct{}{}:
	default:
	}
}

func (h *Handler) dispatch(ctx context.Context, limit int) {
	// Polling also recovers a missed notification or a transient DB error.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	finished := make(chan bool, limit)
	var workers sync.WaitGroup
	defer workers.Wait()
	var active, grading int
	for ctx.Err() == nil {
		for active < limit && ctx.Err() == nil {
			// Reserve one slot for feedback if multiple calls can run. At a
			// limit of one, waiting feedback is selected after the current call.
			job, err := h.store.ClaimEvaluation(limit > 1 && grading >= limit-1)
			if err != nil && ctx.Err() == nil {
				slog.Error("claim evaluation job", "error", err)
			}
			if err != nil || job == nil {
				break
			}
			active++
			if !job.Feedback {
				grading++
			}
			workers.Go(func() {
				h.processEvaluation(ctx, *job)
				finished <- job.Feedback
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-h.workReady:
		case feedback := <-finished:
			active--
			if !feedback {
				grading--
			}
		}
	}
}

func (h *Handler) processEvaluation(ctx context.Context, job store.EvaluationJob) {
	ctx, cancel := context.WithCancel(ctx)
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(store.EvaluationLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ok, err := h.store.RenewEvaluation(job)
				if err != nil {
					slog.Error("renew evaluation lease", "job_id", job.ID, "error", err)
				}
				if !ok {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-renewed }()
	reply, score, evalErr := h.evaluateJob(ctx, job)
	if evalErr != nil && ctx.Err() == nil {
		slog.Warn("evaluation failed", "job_id", job.ID, "thread_id", job.ThreadID, "reason", llm.FailureCode(evalErr))
	}
	for ctx.Err() == nil {
		var err error
		switch {
		case job.Feedback:
			err = h.store.FinishFeedback(job, reply)
		case score != nil:
			err = h.store.FinishGrading(job, *score)
		default:
			var retryAt, deadline int64
			var retry *llm.RetryGrade
			if errors.As(evalErr, &retry) {
				retryAt, deadline = retry.At.UnixMilli(), retry.Deadline.UnixMilli()
			}
			err = h.store.FailGrading(job, llm.FailureCode(evalErr), retryAt, deadline)
		}
		if err == nil {
			return
		}
		// Keep the model's result while retrying a failed database commit.
		slog.Error("save evaluation result", "job_id", job.ID, "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	if err := h.store.ReleaseEvaluation(job); err != nil {
		slog.Error("release interrupted evaluation", "job_id", job.ID, "error", err)
	}
}

// evaluateJob reads saved answers and calls the provider outside any transaction.
// The provider owns transient retries and its timeout budget.
func (h *Handler) evaluateJob(ctx context.Context, job store.EvaluationJob) (*model.Message, *model.QuestionScore, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	thread, err := h.store.GetThread(job.ThreadID)
	if err != nil {
		return nil, nil, err
	}
	session, bp, err := h.store.GetSessionWithBlueprint(job.SessionID)
	if err != nil {
		return nil, nil, err
	}
	if (job.Feedback && session.Status != model.StatusInProgress) || (!job.Feedback && session.Status != model.StatusGrading) {
		return nil, nil, errors.New("evaluation superseded")
	}
	messages, err := h.store.GetMessages(thread.ID)
	if err != nil {
		return nil, nil, err
	}
	if !job.Feedback && len(messages) == 0 {
		return nil, &model.QuestionScore{ThreadID: thread.ID, LLMFeedback: "No answer provided."}, nil
	}
	question, err := h.store.GetQuestion(thread.QuestionID)
	if err != nil {
		return nil, nil, err
	}
	if job.Feedback {
		result, _, err := h.llm.EvaluateAnswer(ctx, question, messages, bp.MaxFollowups, job.SessionID, thread.ID)
		if err != nil {
			return nil, nil, err
		}
		if result == nil {
			return nil, nil, errors.New("evaluator returned no feedback")
		}
		reply := &model.Message{ThreadID: thread.ID, Role: model.RoleLLM, Content: result.Feedback, Score: &result.Score}
		if result.NeedFollowup && thread.FollowupCount < bp.MaxFollowups {
			reply.Followup = strings.TrimSpace(result.FollowupQ)
		}
		return reply, nil, nil
	}
	result, err := h.llm.GradeThreadAttempt(ctx, question, messages, job.SessionID, thread.ID, llm.GradeAttempt{Number: job.Attempt, Deadline: job.RetryDeadline})
	if err != nil {
		return nil, nil, err
	}
	if result == nil {
		return nil, nil, errors.New("evaluator returned no grade")
	}
	return nil, &model.QuestionScore{ThreadID: thread.ID, LLMScore: result.Score, LLMFeedback: result.Feedback}, nil
}
