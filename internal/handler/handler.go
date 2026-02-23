package handler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/handler/views"
	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// Handler holds shared dependencies for HTTP handlers.
type Handler struct {
	store  *store.Store
	llm    *llm.Client
	config model.ExamConfig
}

// New creates a new Handler.
func New(s *store.Store, l *llm.Client, cfg model.ExamConfig) (*Handler, error) {
	return &Handler{store: s, llm: l, config: cfg}, nil
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

// Routes registers all HTTP routes.
func (h *Handler) Routes(r chi.Router) {
	// Public routes.
	r.Get("/login", h.handleLoginPage)
	r.Post("/login", h.handleLogin)

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(h.requireAuth)

		r.Post("/logout", h.handleLogout)
		r.Get("/", h.handleIndex)
		r.Post("/exam/start", h.handleStartExam)
		r.Get("/exam/{sessionID}", h.handleExamPage)
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

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	user := model.UserFromContext(r.Context())

	var sessions []model.ExamSession
	var err error
	if user.Role == model.UserRoleStudent {
		sessions, err = h.store.ListSessionsByUser(user.ID)
	} else {
		sessions, err = h.store.ListSessions()
	}
	if err != nil {
		slog.Error("failed to list sessions", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Get available topics for the dropdown.
	allTopics, err := h.store.ListDistinctTopics()
	if err != nil {
		slog.Error("failed to list topics", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// If --topic is set, restrict topics to only that value.
	var topics []string
	if h.config.Topic != "" {
		for _, t := range allTopics {
			if t == h.config.Topic {
				topics = append(topics, t)
				break
			}
		}
	} else {
		topics = allTopics
	}

	// Count questions matching the configured filters.
	filtered, err := h.store.ListQuestionsFiltered(h.config.Difficulty, h.config.Topic)
	if err != nil {
		slog.Error("failed to list filtered questions", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	availableCount := len(filtered)
	examCount := availableCount
	if h.config.NumQuestions > 0 && h.config.NumQuestions < availableCount {
		examCount = h.config.NumQuestions
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.IndexPage(sessions, availableCount, examCount, h.config, topics).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleStartExam(w http.ResponseWriter, r *http.Request) {
	// Use topic from form (dropdown) if provided, otherwise fall back to CLI flag.
	topic := r.FormValue("topic")
	if topic == "" {
		topic = h.config.Topic
	}

	questions, err := h.store.ListQuestionsFiltered(h.config.Difficulty, topic)
	if err != nil {
		slog.Error("failed to list questions for exam", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(questions) == 0 {
		http.Error(w, "No questions match the configured filters.", http.StatusBadRequest)
		return
	}

	if h.config.Shuffle {
		rand.Shuffle(len(questions), func(i, j int) {
			questions[i], questions[j] = questions[j], questions[i]
		})
	}

	if h.config.NumQuestions > 0 && h.config.NumQuestions < len(questions) {
		questions = questions[:h.config.NumQuestions]
	}

	var questionIDs []int64
	for _, q := range questions {
		questionIDs = append(questionIDs, q.ID)
	}

	user := model.UserFromContext(r.Context())
	sessionID, err := h.store.CreateSession(1, user.ID, questionIDs)
	if err != nil {
		slog.Error("failed to create session", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.path(fmt.Sprintf("/exam/%d", sessionID)), http.StatusSeeOther)
}

func (h *Handler) handleExamPage(w http.ResponseWriter, r *http.Request) {
	sessionID, err := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

	view, err := h.store.GetSessionView(sessionID)
	if err != nil {
		slog.Error("failed to get session view", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user := model.UserFromContext(r.Context())
	if user.Role == model.UserRoleStudent && view.Session.StudentID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ExamPage(*view).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleAnswer(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	threadID, _ := strconv.ParseInt(chi.URLParam(r, "threadID"), 10, 64)

	answer := r.FormValue("answer")
	if answer == "" {
		http.Error(w, "answer cannot be empty", http.StatusBadRequest)
		return
	}

	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		slog.Error("failed to get session", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	_, err = h.store.AddMessage(model.Message{
		ThreadID: threadID,
		Role:     model.RoleStudent,
		Content:  answer,
	})
	if err != nil {
		slog.Error("failed to add student message", "thread_id", threadID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	thread, err := h.store.GetThread(threadID)
	if err != nil {
		slog.Error("failed to get thread", "thread_id", threadID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	question, err := h.store.GetQuestion(thread.QuestionID)
	if err != nil {
		slog.Error("failed to get question", "question_id", thread.QuestionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	messages, err := h.store.GetMessages(threadID)
	if err != nil {
		slog.Error("failed to get messages", "thread_id", threadID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	bp, err := h.store.GetBlueprint(sess.BlueprintID)
	if err != nil {
		slog.Error("failed to get blueprint", "blueprint_id", sess.BlueprintID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result, _, err := h.llm.EvaluateAnswer(context.Background(), question, messages, bp.MaxFollowups, sessionID, threadID)
	if err != nil {
		slog.Error("LLM evaluation failed", "error", err)
		http.Error(w, "LLM evaluation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	llmText := result.Feedback
	if result.NeedFollowup && result.FollowupQ != "" {
		llmText += "\n\n**Follow-up question:** " + result.FollowupQ
	}

	_, err = h.store.AddMessage(model.Message{
		ThreadID: threadID,
		Role:     model.RoleLLM,
		Content:  llmText,
	})
	if err != nil {
		slog.Error("failed to add LLM message", "thread_id", threadID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ThreadContent(updatedThread, question, updatedMessages, sessionID, threadIndex, sess).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleSubmit(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)

	if err := h.store.UpdateSessionStatus(sessionID, model.StatusSubmitted); err != nil {
		slog.Error("failed to update session to submitted", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateSessionStatus(sessionID, model.StatusGrading); err != nil {
		slog.Error("failed to update session to grading", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	threads, err := h.store.GetThreadsForSession(sessionID)
	if err != nil {
		slog.Error("failed to get threads for grading", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

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

		result, err := h.llm.GradeThread(context.Background(), question, messages, sessionID, t.ID)
		if err != nil {
			slog.Error("grading failed", "thread_id", t.ID, "error", err)
			if err := h.store.UpsertScore(model.QuestionScore{
				ThreadID:    t.ID,
				LLMScore:    0,
				LLMFeedback: "Grading error: " + err.Error(),
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
	sessionID, err := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

	view, err := h.store.GetSessionView(sessionID)
	if err != nil {
		slog.Error("failed to get session view", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user := model.UserFromContext(r.Context())
	if view.Session.StudentID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ResultsPage(*view).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleReviewList(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.store.ListSessions()
	if err != nil {
		slog.Error("failed to list sessions for review", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	sessionID, _ := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)

	view, err := h.store.GetSessionView(sessionID)
	if err != nil {
		slog.Error("failed to get session view for review", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ReviewPage(*view).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleUpdateScore(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	threadID, _ := strconv.ParseInt(chi.URLParam(r, "threadID"), 10, 64)

	scoreStr := r.FormValue("teacher_score")
	comment := r.FormValue("teacher_comment")

	score, err := strconv.ParseFloat(scoreStr, 64)
	if err != nil {
		http.Error(w, "invalid score", http.StatusBadRequest)
		return
	}

	if err := h.store.UpdateTeacherScore(threadID, score, comment); err != nil {
		slog.Error("failed to update teacher score", "thread_id", threadID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.path(fmt.Sprintf("/review/%d", sessionID)), http.StatusSeeOther)
}

func (h *Handler) handleFinalize(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)

	gradeStr := r.FormValue("final_grade")
	finalGrade, err := strconv.ParseFloat(gradeStr, 64)
	if err != nil {
		http.Error(w, "invalid grade", http.StatusBadRequest)
		return
	}

	user := model.UserFromContext(r.Context())
	if err := h.store.FinalizeGrade(sessionID, finalGrade, user.ID); err != nil {
		slog.Error("failed to finalize grade", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateSessionStatus(sessionID, model.StatusReviewed); err != nil {
		slog.Error("failed to update session to reviewed", "session_id", sessionID, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, h.path(fmt.Sprintf("/review/%d", sessionID)), http.StatusSeeOther)
}
