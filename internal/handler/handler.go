package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

// Evaluator is the part of the LLM client the exam handlers depend on.
// *llm.Client implements it; tests use a fake.
type Evaluator interface {
	EvaluateAnswer(ctx context.Context, question model.Question, messages []model.Message, maxFollowups int, sessionID, threadID int64) (*llm.GradeResult, string, error)
	GradeThread(ctx context.Context, question model.Question, messages []model.Message, sessionID, threadID int64) (*llm.GradeResult, error)
}

// Handler holds shared dependencies for HTTP handlers.
type Handler struct {
	store          *store.Store
	llm            Evaluator
	config         model.ExamConfig
	questionSchema *jsonschema.Schema
	registration   registrationAdmission

	// inFlight is the set of thread IDs with an answer being processed, so
	// only one answer per thread runs at a time. Entries are removed when the
	// request ends. In-process only: enough for a single replica; the
	// SessionStore transition replaces it.
	inFlightMu sync.Mutex
	inFlight   map[int64]struct{}
}

// New creates a new Handler.
func New(s *store.Store, l Evaluator, cfg model.ExamConfig) (*Handler, error) {
	schema, err := compileQuestionSchema()
	if err != nil {
		return nil, fmt.Errorf("compile question schema: %w", err)
	}
	return &Handler{store: s, llm: l, config: cfg, questionSchema: schema}, nil
}

func compileQuestionSchema() (*jsonschema.Schema, error) {
	absSchema, err := filepath.Abs("schema/question_schema.json")
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	f, err := os.Open(absSchema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	schemaURL := "file://" + filepath.ToSlash(absSchema)
	if err := compiler.AddResource(schemaURL, f); err != nil {
		return nil, err
	}
	return compiler.Compile(schemaURL)
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
		r.Post("/exam/{sessionID}/submit", h.handleSubmit)
		r.Get("/results/{sessionID}", h.handleStudentResults)

		// Teacher + admin routes.
		r.Group(func(r chi.Router) {
			r.Use(requireRole(model.UserRoleTeacher, model.UserRoleAdmin))
			r.Get("/review", h.handleReviewList)
			r.Get("/review/{sessionID}", h.handleReviewPage)
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

// beginAnswer marks the thread as busy; it returns false if it already is.
func (h *Handler) beginAnswer(threadID int64) bool {
	h.inFlightMu.Lock()
	defer h.inFlightMu.Unlock()
	if _, busy := h.inFlight[threadID]; busy {
		return false
	}
	if h.inFlight == nil {
		h.inFlight = make(map[int64]struct{})
	}
	h.inFlight[threadID] = struct{}{}
	return true
}

func (h *Handler) endAnswer(threadID int64) {
	h.inFlightMu.Lock()
	defer h.inFlightMu.Unlock()
	delete(h.inFlight, threadID)
}

func (h *Handler) handleAnswer(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}
	threadID, ok := parseIDParam(w, r, "threadID")
	if !ok {
		return
	}

	answer := r.FormValue("answer")
	if answer == "" {
		http.Error(w, "answer cannot be empty", http.StatusBadRequest)
		return
	}

	sess, bp, err := h.store.GetSessionWithBlueprint(sessionID)
	if err != nil {
		lookupError(w, err, "failed to get session with blueprint", "session_id", sessionID)
		return
	}

	user := model.UserFromContext(r.Context())
	if user.Role == model.UserRoleStudent && sess.StudentID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if sess.Status != model.StatusInProgress {
		http.Error(w, "exam already submitted", http.StatusBadRequest)
		return
	}

	// Check time limit.
	if calculateTimeRemaining(sess, bp) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<p class="time-exceeded-error">Time limit exceeded. Please submit your exam.</p>`)
		return
	}

	// One answer per thread at a time: a double click or a second tab gets
	// 409 instead of racing the first request past the follow-up limit.
	if !h.beginAnswer(threadID) {
		http.Error(w, "an answer to this question is already being evaluated", http.StatusConflict)
		return
	}
	defer h.endAnswer(threadID)

	thread, err := h.store.GetThread(threadID)
	if err != nil {
		lookupError(w, err, "failed to get thread", "thread_id", threadID)
		return
	}

	if thread.SessionID != sessionID {
		http.Error(w, "thread does not belong to session", http.StatusForbidden)
		return
	}

	if thread.Status == model.ThreadCompleted {
		http.Error(w, "this question is already completed", http.StatusConflict)
		return
	}

	messages, err := h.store.GetMessages(threadID)
	if err != nil {
		slog.Error("failed to get messages", "thread_id", threadID, "error", err)
		http.Error(w, "failed to load answer", http.StatusInternalServerError)
		return
	}
	pending := len(messages) > 0 && messages[len(messages)-1].Role == model.RoleStudent
	if pending && messages[len(messages)-1].Content != answer {
		http.Error(w, "a saved answer is awaiting evaluation; retry that answer", http.StatusConflict)
		return
	}
	if !pending {
		_, err = h.store.AddMessage(model.Message{ThreadID: threadID, Role: model.RoleStudent, Content: answer})
		if err == nil {
			messages, err = h.store.GetMessages(threadID)
		}
		if err != nil {
			slog.Error("failed to save student message", "thread_id", threadID, "error", err)
			http.Error(w, "failed to save answer", http.StatusInternalServerError)
			return
		}
	}

	question, err := h.store.GetQuestion(thread.QuestionID)
	if err != nil {
		slog.Error("failed to get question", "question_id", thread.QuestionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	result, _, err := h.llm.EvaluateAnswer(r.Context(), question, messages, bp.MaxFollowups, sessionID, threadID)
	if err != nil {
		slog.Error("LLM evaluation failed", "error", err)
		http.Error(w, "LLM evaluation failed", http.StatusInternalServerError)
		return
	}

	// The limit lives in the prompt only as a request; enforce it here.
	if result.NeedFollowup && strings.TrimSpace(result.FollowupQ) == "" {
		result.NeedFollowup = false
	}
	if result.NeedFollowup && thread.FollowupCount >= bp.MaxFollowups {
		slog.Warn("LLM requested follow-up beyond limit",
			"session_id", sessionID,
			"thread_id", threadID,
			"followup_count", thread.FollowupCount,
			"max_followups", bp.MaxFollowups,
		)
		result.NeedFollowup = false
		result.FollowupQ = ""
	}

	llmMsg := model.Message{
		ThreadID: threadID,
		Role:     model.RoleLLM,
		Content:  result.Feedback,
		Score:    &result.Score,
	}
	if result.NeedFollowup {
		llmMsg.Followup = result.FollowupQ
	}

	_, err = h.store.AddMessage(llmMsg)
	if err != nil {
		slog.Error("failed to add LLM message", "thread_id", threadID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	newStatus := model.ThreadAnswered
	if !result.NeedFollowup {
		newStatus = model.ThreadCompleted
	}
	if err := h.store.UpdateThreadStatus(threadID, newStatus); err != nil {
		slog.Warn("failed to update thread status", "thread_id", threadID, "status", newStatus, "error", err)
	}

	updatedMessages, err := h.store.GetMessages(threadID)
	if err != nil {
		slog.Warn("failed to get updated messages", "thread_id", threadID, "error", err)
	}
	updatedThread, err := h.store.GetThread(threadID)
	if err != nil {
		slog.Warn("failed to get updated thread", "thread_id", threadID, "error", err)
	}

	allThreads, err := h.store.GetThreadsForSession(sessionID)
	if err != nil {
		slog.Warn("failed to get threads for session", "session_id", sessionID, "error", err)
	}
	threadIndex := 0
	for i, t := range allThreads {
		if t.ID == threadID {
			threadIndex = i
			break
		}
	}

	// Recalculate time status for accurate UI rendering after LLM evaluation.
	timeExceeded := calculateTimeRemaining(sess, bp) == 0

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ThreadContent(updatedThread, question, updatedMessages, sessionID, threadIndex, sess, timeExceeded, h.config.ScoreVisibility.LiveScorePolicy(sess.Status)).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}

	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		lookupError(w, err, "failed to get session", "session_id", sessionID)
		return
	}

	user := model.UserFromContext(r.Context())
	if user.Role == model.UserRoleStudent && sess.StudentID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if sess.Status != model.StatusInProgress {
		http.Error(w, "exam already submitted", http.StatusConflict)
		return
	}

	if err := h.store.UpdateSessionStatus(sessionID, model.StatusSubmitted); err != nil {
		slog.Error("failed to update session to submitted", "session_id", sessionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateSessionStatus(sessionID, model.StatusGrading); err != nil {
		slog.Error("failed to update session to grading", "session_id", sessionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	threads, err := h.store.GetThreadsForSession(sessionID)
	if err != nil {
		slog.Error("failed to get threads for grading", "session_id", sessionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Финальная оценка не отменяется при закрытии браузера; каждый вызов
	// ограничен бюджетом llm.Client. Постоянная очередь реализуется в #48.
	gradingCtx := context.WithoutCancel(r.Context())
	var totalScore float64
	var totalMaxPoints int

	for _, t := range threads {
		question, err := h.store.GetQuestion(t.QuestionID)
		if err != nil {
			continue
		}
		messages, err := h.store.GetMessages(t.ID)
		if err != nil || len(messages) == 0 {
			if err := h.store.UpsertScore(model.QuestionScore{
				ThreadID:    t.ID,
				LLMScore:    0,
				LLMFeedback: "No answer provided.",
			}); err != nil {
				slog.Warn("failed to upsert zero score", "thread_id", t.ID, "error", err)
			}
			totalMaxPoints += question.MaxPoints
			continue
		}

		result, err := h.llm.GradeThread(gradingCtx, question, messages, sessionID, t.ID)
		if err != nil {
			slog.Error("grading failed", "thread_id", t.ID, "error", err)
			if errors.Is(err, llm.ErrQueueTimeout) {
				if restoreErr := h.store.UpdateSessionStatus(sessionID, model.StatusInProgress); restoreErr != nil {
					slog.Error("failed to restore exam after capacity timeout", "session_id", sessionID, "error", restoreErr)
					http.Error(w, "failed to restore exam", http.StatusInternalServerError)
					return
				}
				http.Error(w, appI18n.T(r.Context(), "LLMQueueBusy"), http.StatusServiceUnavailable)
				return
			}
			if err := h.store.UpsertScore(model.QuestionScore{
				ThreadID:    t.ID,
				LLMScore:    0,
				LLMFeedback: "Automatic grading failed.",
			}); err != nil {
				slog.Warn("failed to upsert error score", "thread_id", t.ID, "error", err)
			}
			totalMaxPoints += question.MaxPoints
			continue
		}

		if err := h.store.UpsertScore(model.QuestionScore{
			ThreadID:    t.ID,
			LLMScore:    result.Score,
			LLMFeedback: result.Feedback,
		}); err != nil {
			slog.Warn("failed to upsert score", "thread_id", t.ID, "error", err)
		}
		if err := h.store.UpdateThreadStatus(t.ID, model.ThreadCompleted); err != nil {
			slog.Warn("failed to update thread to completed", "thread_id", t.ID, "error", err)
		}

		totalScore += result.Score
		totalMaxPoints += question.MaxPoints
	}

	overallGrade := 0.0
	if totalMaxPoints > 0 {
		overallGrade = (totalScore / float64(totalMaxPoints)) * 100
	}

	if err := h.store.UpsertGrade(model.Grade{
		SessionID: sessionID,
		LLMGrade:  overallGrade,
	}); err != nil {
		slog.Warn("failed to upsert grade", "session_id", sessionID, "error", err)
	}
	if err := h.store.UpdateSessionStatus(sessionID, model.StatusGraded); err != nil {
		slog.Warn("failed to update session to graded", "session_id", sessionID, "error", err)
	}

	http.Redirect(w, r, h.path(fmt.Sprintf("/results/%d", sessionID)), http.StatusSeeOther)
}

func (h *Handler) handleStudentResults(w http.ResponseWriter, r *http.Request) {
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
	if view.Session.StudentID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
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

	var reviewable []model.ExamSession
	for _, s := range sessions {
		if s.Status == model.StatusGraded || s.Status == model.StatusReviewed {
			reviewable = append(reviewable, s)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ReviewListPage(reviewable).Render(r.Context(), w); err != nil {
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ReviewPage(*view).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
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
		slog.Error("failed to finalize grade", "session_id", sessionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateSessionStatus(sessionID, model.StatusReviewed); err != nil {
		slog.Error("failed to update session to reviewed", "session_id", sessionID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.path(fmt.Sprintf("/review/%d", sessionID)), http.StatusSeeOther)
}
