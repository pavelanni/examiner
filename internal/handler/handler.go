package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/handler/views"
	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// Evaluator is the part of the LLM client the exam handlers depend on.
// *llm.Client implements it; tests use a fake.
type Evaluator interface {
	EvaluateAnswer(ctx context.Context, question model.Question, messages []model.Message, maxFollowups int, sessionID, threadID int64) (*llm.GradeResult, string, error)
	GradeThreadAttempt(ctx context.Context, question model.Question, messages []model.Message, sessionID, threadID int64, attempt llm.GradeAttempt) (*llm.GradeResult, error)
}

// Handler holds shared dependencies for HTTP handlers.
type Handler struct {
	store           *store.Store
	llm             Evaluator
	config          model.ExamConfig
	registration    registrationAdmission
	teacherUploadMu sync.Mutex

	// Initialized before serving. The durable jobs in SQLite are the source
	// of truth; the notification just wakes an idle dispatcher.
	workReady chan struct{}
}

// New creates a new Handler. The error is kept for callers; nothing in
// construction can fail today.
func New(s *store.Store, l Evaluator, cfg model.ExamConfig) (*Handler, error) {
	return &Handler{store: s, llm: l, config: cfg}, nil
}

// calculateTimeRemaining returns remaining exam time.
// Returns -1 if no limit is set, 0 if the limit has been exceeded.
func calculateTimeRemaining(session model.ExamSession, blueprint model.ExamBlueprint) time.Duration {
	if blueprint.TimeLimit <= 0 {
		return -1 // no limit
	}
	limit := time.Duration(blueprint.TimeLimit) * time.Minute
	elapsed := time.Since(session.StartedAt)
	remaining := limit - elapsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// BasePathMiddleware injects the base path into the request context.
func (h *Handler) BasePathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := model.ContextWithBasePath(r.Context(), h.config.BasePath)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// path prepends the base path to the given path.
func (h *Handler) path(p string) string {
	return h.config.BasePath + p
}

// parseIDParam reads a numeric URL parameter. On a malformed value it
// writes 400 and returns false.
func parseIDParam(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid "+name, http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// lookupError answers a failed lookup of a record named in the URL:
// 404 when it does not exist, otherwise 500 with the detail logged only.
func lookupError(w http.ResponseWriter, err error, msg string, args ...any) {
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Error(msg, append(args, "error", err)...)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// Routes registers all HTTP routes.
func (h *Handler) Routes(r chi.Router) {
	// Public routes (login).
	r.Group(func(r chi.Router) {
		r.Use(h.csrfMiddleware)
		r.Get("/login", h.handleLoginPage)
		r.Post("/login", h.handleLogin)
	})

	if h.config.AllowRegistration {
		r.Group(func(r chi.Router) {
			r.Use(registrationFormLimit, h.csrfMiddleware)
			r.Get("/register", h.handleRegisterPage)
			r.Post("/register", h.handleRegister)
		})
	}

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(h.requireAuth)
		r.Use(h.csrfMiddleware)

		r.Post("/logout", h.handleLogout)
		r.Get("/", h.handleIndex)
		r.Get("/exam/{sessionID}", h.handleExamPage)
		r.Post("/exam/start", h.handleStartExam)
		r.Post("/exam/{sessionID}/answer/{threadID}", h.handleAnswer)
		r.Get("/exam/{sessionID}/answer/{threadID}", h.handleFeedback)
		r.Post("/exam/{sessionID}/submit", h.handleSubmit)
		r.Get("/results/{sessionID}", h.handleStudentResults)

		// Teacher + admin routes.
		r.Group(func(r chi.Router) {
			r.Use(requireRole(model.UserRoleTeacher, model.UserRoleAdmin))
			r.Get("/review", h.handleReviewList)
			r.Get("/review/export", h.handleStatement)
			r.Get("/review/{sessionID}", h.handleReviewPage)
			r.Post("/review/{sessionID}/retry-grading", h.handleRetryGrading)
			r.Get("/review/{sessionID}/reset", h.handleResetAttemptPage)
			r.Post("/review/{sessionID}/reset", h.handleResetAttempt)
			r.Post("/review/{sessionID}/score/{threadID}", h.handleUpdateScore)
			r.Post("/review/{sessionID}/finalize", h.handleFinalize)
			r.Get("/teacher/me", h.handleTeacherMe)
			r.Get("/teacher/profile", h.handleTeacherProfile)
			r.Get("/teacher/create-test", h.handleTeacherCreateTest)
			r.Post("/teacher/tests", h.handleTeacherUpload)
			r.Get("/teacher/tests/file/{name}", h.handleTeacherDownload)
		})

		// Admin-only routes.
		r.Group(func(r chi.Router) {
			r.Use(requireRole(model.UserRoleAdmin))
			r.Get("/admin/users", h.handleAdminUsersPage)
			r.Post("/admin/users", h.handleCreateUser)
			r.Post("/admin/users/{userID}/toggle", h.handleToggleUserActive)
			r.Get("/admin/questions", h.handleAdminQuestionsPage)
			r.Post("/admin/questions", h.handleUploadQuestions)
		})
	})
}

func (h *Handler) handleExamPage(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}

	view, err := h.store.GetSessionView(sessionID)
	if err != nil {
		lookupError(w, err, "failed to get session view", "session_id", sessionID)
		return
	}

	user := model.UserFromContext(r.Context())
	if user.Role == model.UserRoleStudent && view.Session.StudentID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	timeRemaining := calculateTimeRemaining(view.Session, view.Blueprint)
	pageView := model.ExamPageView{
		SessionView:   *view,
		TimeRemaining: timeRemaining,
		TimeExceeded:  timeRemaining == 0,
		LiveScore:     h.config.ScoreVisibility.LiveScorePolicy(view.Session.Status),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ExamPage(pageView).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleStudentResults(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}

	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		lookupError(w, err, "load results session", "session_id", sessionID)
		return
	}
	user := model.UserFromContext(r.Context())
	if sess.StudentID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// Only skip the swap when the browser already displays this phase.
	observedFailure := r.URL.Query().Get("status") == string(model.StatusGradingFailed)
	if r.Header.Get("HX-Request") == "true" && ((!observedFailure && sess.Status.GradingPending()) ||
		(observedFailure && sess.Status == model.StatusGradingFailed)) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	view, err := h.store.GetSessionView(sessionID)
	if err != nil {
		lookupError(w, err, "failed to get session view", "session_id", sessionID)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ResultsPage(*view, h.config.ScoreVisibility.ResultsPolicy(view.Session.Status)).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleReviewList(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.store.ListSessions()
	if err != nil {
		slog.Error("failed to list sessions for review", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	topics, err := h.store.ListPublishedTopics()
	if err != nil {
		slog.Error("failed to list statement topics", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Keep historical exam topics selectable after their questions are unpublished.
	topicSet := make(map[string]struct{}, len(topics))
	for _, topic := range topics {
		topicSet[topic] = struct{}{}
	}
	for _, session := range sessions {
		if session.SubmittedAt != nil && session.TopicKnown {
			topicSet[session.Topic] = struct{}{}
		}
	}
	topics = topics[:0]
	for topic := range topicSet {
		if topic != "" {
			topics = append(topics, topic)
		}
	}
	sort.Strings(topics)

	var reviewable, other []model.ExamSession
	for _, s := range sessions {
		if s.Status == model.StatusGraded || s.Status == model.StatusReviewed || s.Status == model.StatusGradingFailed {
			reviewable = append(reviewable, s)
		} else {
			other = append(other, s)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ReviewListPage(reviewable, other, topics, r.URL.Query()).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleReviewPage(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}

	view, err := h.store.GetSessionView(sessionID)
	if err != nil {
		lookupError(w, err, "failed to get session view for review", "session_id", sessionID)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	progress, err := h.store.GradingProgress(sessionID)
	if err != nil {
		lookupError(w, err, "load grading progress", "session_id", sessionID)
		return
	}
	if err := views.ReviewPage(*view, progress).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleRetryGrading(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}
	if err := h.store.RetryGrading(id); err != nil {
		if errors.Is(err, store.ErrGradingNotFailed) {
			http.Error(w, i18n.T(r.Context(), "RetryGradingConflict"), http.StatusConflict)
			return
		}
		lookupError(w, err, "retry final grading", "session_id", id)
		return
	}
	h.wakeEvaluation()
	http.Redirect(w, r, h.path(fmt.Sprintf("/review/%d", id)), http.StatusSeeOther)
}

func (h *Handler) handleUpdateScore(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}
	threadID, ok := parseIDParam(w, r, "threadID")
	if !ok {
		return
	}

	score, err := strconv.ParseFloat(r.FormValue("teacher_score"), 64)
	if err != nil {
		http.Error(w, "invalid score", http.StatusBadRequest)
		return
	}
	comment := r.FormValue("teacher_comment")

	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		lookupError(w, err, "failed to get session for score update", "session_id", sessionID)
		return
	}
	// Scores exist only once the session is graded, and a reviewed session
	// is final: changing it would silently alter a confirmed result.
	if sess.Status != model.StatusGraded {
		http.Error(w, "session is not awaiting review", http.StatusConflict)
		return
	}

	thread, err := h.store.GetThread(threadID)
	if err != nil {
		lookupError(w, err, "failed to get thread for score update", "thread_id", threadID)
		return
	}
	if thread.SessionID != sessionID {
		http.Error(w, "thread does not belong to session", http.StatusForbidden)
		return
	}

	question, err := h.store.GetQuestion(thread.QuestionID)
	if err != nil {
		slog.Error("failed to get question for score update", "question_id", thread.QuestionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Written as a negated range so NaN, which fails every comparison, is rejected too.
	if !(score >= 0 && score <= float64(question.MaxPoints)) {
		http.Error(w, "score out of range", http.StatusBadRequest)
		return
	}

	if err := h.store.UpdateTeacherScore(threadID, score, comment); err != nil {
		if sessionResetConflict(w, r, err) {
			return
		}
		slog.Error("failed to update teacher score", "thread_id", threadID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.path(fmt.Sprintf("/review/%d", sessionID)), http.StatusSeeOther)
}

func (h *Handler) handleFinalize(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}

	finalGrade, err := strconv.ParseFloat(r.FormValue("final_grade"), 64)
	if err != nil {
		http.Error(w, "invalid grade", http.StatusBadRequest)
		return
	}
	// The final grade is a percentage. Negated range rejects NaN as well.
	if !(finalGrade >= 0 && finalGrade <= 100) {
		http.Error(w, "grade out of range", http.StatusBadRequest)
		return
	}

	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		lookupError(w, err, "failed to get session for finalize", "session_id", sessionID)
		return
	}
	// Only a graded session can be confirmed: earlier there is nothing to
	// confirm, and confirming again would overwrite the first reviewer.
	if sess.Status != model.StatusGraded {
		http.Error(w, "session is not awaiting review", http.StatusConflict)
		return
	}

	user := model.UserFromContext(r.Context())
	if err := h.store.FinalizeGrade(sessionID, finalGrade, user.ID); err != nil {
		if sessionResetConflict(w, r, err) {
			return
		}
		slog.Error("failed to finalize grade", "session_id", sessionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateSessionStatus(sessionID, model.StatusReviewed); err != nil {
		if sessionResetConflict(w, r, err) {
			return
		}
		slog.Error("failed to update session to reviewed", "session_id", sessionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.path(fmt.Sprintf("/review/%d", sessionID)), http.StatusSeeOther)
}
