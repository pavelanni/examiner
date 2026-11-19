package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/pavelanni/examiner/internal/handler/views"
	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

type answerThread struct {
	model.ThreadView
	session   model.ExamSession
	blueprint model.ExamBlueprint
}

func (h *Handler) loadAnswerThread(w http.ResponseWriter, r *http.Request) (*answerThread, bool) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return nil, false
	}
	threadID, ok := parseIDParam(w, r, "threadID")
	if !ok {
		return nil, false
	}
	session, bp, err := h.store.GetSessionWithBlueprint(sessionID)
	if err != nil {
		lookupError(w, r, err, "load answer session", "id", sessionID)
		return nil, false
	}
	user := model.UserFromContext(r.Context())
	if user.Role == model.UserRoleStudent && session.StudentID != user.ID {
		httpError(w, r, http.StatusForbidden, "ErrForbidden")
		return nil, false
	}
	thread, err := h.store.GetThread(threadID)
	if err != nil {
		lookupError(w, r, err, "load answer thread", "id", threadID)
		return nil, false
	}
	if thread.SessionID != sessionID {
		httpError(w, r, http.StatusForbidden, "ErrForbidden")
		return nil, false
	}
	return &answerThread{ThreadView: model.ThreadView{Thread: thread}, session: session, blueprint: bp}, true
}

// handleAnswer returns an acknowledgement once the answer and job are durable.
func (h *Handler) handleAnswer(w http.ResponseWriter, r *http.Request) {
	a, ok := h.loadAnswerThread(w, r)
	if !ok {
		return
	}
	if a.session.Status == model.StatusAnnulled {
		sessionResetConflict(w, r, store.ErrSessionAnnulled)
		return
	}
	if a.session.Status != model.StatusInProgress || a.Thread.Status == model.ThreadCompleted || a.Thread.Status == model.ThreadEvaluating {
		h.feedbackConflict(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		answerFormError(w, r, "FeedbackInvalidForm", http.StatusBadRequest)
		return
	}
	turn, err := strconv.ParseInt(r.PostForm.Get("answer_key"), 10, 64)
	if err != nil || turn < 0 {
		h.feedbackConflict(w, r)
		return
	}
	retry := r.PostForm.Get("retry") == "1"
	answer := r.PostForm.Get("answer")
	if !retry && strings.TrimSpace(answer) == "" {
		answerFormError(w, r, "FeedbackEmpty", http.StatusBadRequest)
		return
	}
	if !retry && calculateTimeRemaining(a.session, a.blueprint) == 0 {
		answerFormError(w, r, "FeedbackTimeExpired", http.StatusForbidden)
		return
	}
	// Capture the form's turn for the acknowledgement. QueueAnswer verifies it
	// again under the write lock, even if another tab has already advanced it.
	if err := h.loadConversation(a); err != nil {
		lookupError(w, r, err, "load answer view")
		return
	}
	err = h.store.QueueAnswer(a.session.ID, a.Thread.ID, turn, answer, retry)
	if errors.Is(err, store.ErrTurnChanged) {
		h.feedbackConflict(w, r)
		return
	}
	if err != nil {
		lookupError(w, r, err, "queue answer", "thread_id", a.Thread.ID)
		return
	}
	h.wakeEvaluation()
	if r.Header.Get("HX-Request") != "true" && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, h.path(fmt.Sprintf("/exam/%d#thread-%d", a.session.ID, a.Thread.ID)), http.StatusSeeOther)
		return
	}
	if !retry {
		a.Messages = append(a.Messages, model.Message{ThreadID: a.Thread.ID, Role: model.RoleStudent, Content: answer})
	}
	a.Thread.Status = model.ThreadEvaluating
	h.renderAnswerThread(w, r, a, http.StatusAccepted)
}

func answerFormError(w http.ResponseWriter, r *http.Request, message string, status int) {
	w.Header().Set("X-Feedback-Error", "1")
	http.Error(w, i18n.T(r.Context(), message), status)
}

// A conflict updates only this question. Other unsent answers stay in the page.
func (h *Handler) feedbackConflict(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("X-Feedback-Conflict", "1")
		h.showFeedback(w, r, http.StatusConflict)
		return
	}
	http.Error(w, i18n.T(r.Context(), "FeedbackStale"), http.StatusConflict)
}

func (h *Handler) handleFeedback(w http.ResponseWriter, r *http.Request) {
	h.showFeedback(w, r, http.StatusOK)
}

func (h *Handler) showFeedback(w http.ResponseWriter, r *http.Request, code int) {
	a, ok := h.loadAnswerThread(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if code == http.StatusOK && r.Header.Get("HX-Request") == "true" && a.Thread.Status == model.ThreadEvaluating && a.session.Status == model.StatusInProgress {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.loadConversation(a); err != nil {
		lookupError(w, r, err, "load feedback view")
		return
	}
	h.renderAnswerThread(w, r, a, code)
}

func (h *Handler) loadConversation(a *answerThread) error {
	var err error
	a.Question, err = h.store.GetQuestion(a.Thread.QuestionID)
	if err != nil {
		return err
	}
	a.Messages, err = h.store.GetMessages(a.Thread.ID)
	return err
}

func (h *Handler) renderAnswerThread(w http.ResponseWriter, r *http.Request, a *answerThread, code int) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := views.ThreadFeedback(a.Thread, a.Question, a.Messages, a.session, calculateTimeRemaining(a.session, a.blueprint) == 0, h.config.ScoreVisibility.LiveScorePolicy(a.session.Status)).Render(r.Context(), w); err != nil {
		slog.Error("render thread", "error", err)
	}
}

func (h *Handler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}
	sess, err := h.store.GetSession(id)
	if err != nil {
		lookupError(w, r, err, "load submission", "id", id)
		return
	}
	user := model.UserFromContext(r.Context())
	if user.Role == model.UserRoleStudent && sess.StudentID != user.ID {
		httpError(w, r, http.StatusForbidden, "ErrForbidden")
		return
	}
	if sess.Status == model.StatusAnnulled {
		sessionResetConflict(w, r, store.ErrSessionAnnulled)
		return
	}
	if err := h.store.QueueSubmission(id); err != nil {
		if errors.Is(err, store.ErrTurnChanged) {
			http.Error(w, i18n.T(r.Context(), "ExamSubmitted"), http.StatusConflict)
			return
		}
		lookupError(w, r, err, "queue final grade", "id", id)
		return
	}
	h.wakeEvaluation()
	http.Redirect(w, r, h.path(fmt.Sprintf("/results/%d", id)), http.StatusSeeOther)
}
