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

// Routes registers all HTTP routes.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/", h.handleIndex)
	r.Post("/exam/start", h.handleStartExam)
	r.Get("/exam/{sessionID}", h.handleExamPage)
	r.Post("/exam/{sessionID}/answer/{threadID}", h.handleAnswer)
	r.Post("/exam/{sessionID}/submit", h.handleSubmit)
	r.Get("/review", h.handleReviewList)
	r.Get("/review/{sessionID}", h.handleReviewPage)
	r.Post("/review/{sessionID}/score/{threadID}", h.handleUpdateScore)
	r.Post("/review/{sessionID}/finalize", h.handleFinalize)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.store.ListSessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Count questions matching the configured filters.
	filtered, err := h.store.ListQuestionsFiltered(h.config.Difficulty, h.config.Topic)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	availableCount := len(filtered)
	examCount := availableCount
	if h.config.NumQuestions > 0 && h.config.NumQuestions < availableCount {
		examCount = h.config.NumQuestions
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.IndexPage(sessions, availableCount, examCount, h.config).Render(r.Context(), w); err != nil {
		slog.Error("render error", "error", err)
	}
}

func (h *Handler) handleStartExam(w http.ResponseWriter, r *http.Request) {
	questions, err := h.store.ListQuestionsFiltered(h.config.Difficulty, h.config.Topic)
	if err != nil {
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

	sessionID, err := h.store.CreateSession(1, questionIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/exam/%d", sessionID), http.StatusSeeOther)
}

func (h *Handler) handleExamPage(w http.ResponseWriter, r *http.Request) {
	sessionID, err := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

	view, err := h.store.GetSessionView(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	thread, err := h.store.GetThread(threadID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	question, err := h.store.GetQuestion(thread.QuestionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	messages, err := h.store.GetMessages(threadID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	bp, err := h.store.GetBlueprint(sess.BlueprintID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result, _, err := h.llm.EvaluateAnswer(context.Background(), question, messages, bp.MaxFollowups)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	newStatus := model.ThreadAnswered
	if !result.NeedFollowup {
		newStatus = model.ThreadCompleted
	}
	_ = h.store.UpdateThreadStatus(threadID, newStatus)

	updatedMessages, _ := h.store.GetMessages(threadID)
	updatedThread, _ := h.store.GetThread(threadID)

	allThreads, _ := h.store.GetThreadsForSession(sessionID)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateSessionStatus(sessionID, model.StatusGrading); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	threads, err := h.store.GetThreadsForSession(sessionID)
	if err != nil {
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
			_ = h.store.UpsertScore(model.QuestionScore{
				ThreadID:    t.ID,
				LLMScore:    0,
				LLMFeedback: "No answer provided.",
			})
			totalMaxPoints += question.MaxPoints
			continue
		}

		result, err := h.llm.GradeThread(context.Background(), question, messages)
		if err != nil {
			slog.Error("grading failed", "thread_id", t.ID, "error", err)
			_ = h.store.UpsertScore(model.QuestionScore{
				ThreadID:    t.ID,
				LLMScore:    0,
				LLMFeedback: "Grading error: " + err.Error(),
			})
			totalMaxPoints += question.MaxPoints
			continue
		}

		_ = h.store.UpsertScore(model.QuestionScore{
			ThreadID:    t.ID,
			LLMScore:    result.Score,
			LLMFeedback: result.Feedback,
		})
		_ = h.store.UpdateThreadStatus(t.ID, model.ThreadCompleted)

		totalScore += result.Score
		totalMaxPoints += question.MaxPoints
	}

	overallGrade := 0.0
	if totalMaxPoints > 0 {
		overallGrade = (totalScore / float64(totalMaxPoints)) * 100
	}

	_ = h.store.UpsertGrade(model.Grade{
		SessionID: sessionID,
		LLMGrade:  overallGrade,
	})
	_ = h.store.UpdateSessionStatus(sessionID, model.StatusGraded)

	http.Redirect(w, r, fmt.Sprintf("/review/%d", sessionID), http.StatusSeeOther)
}

func (h *Handler) handleReviewList(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.store.ListSessions()
	if err != nil {
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/review/%d", sessionID), http.StatusSeeOther)
}

func (h *Handler) handleFinalize(w http.ResponseWriter, r *http.Request) {
	sessionID, _ := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)

	gradeStr := r.FormValue("final_grade")
	finalGrade, err := strconv.ParseFloat(gradeStr, 64)
	if err != nil {
		http.Error(w, "invalid grade", http.StatusBadRequest)
		return
	}

	if err := h.store.FinalizeGrade(sessionID, finalGrade); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateSessionStatus(sessionID, model.StatusReviewed); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/review/%d", sessionID), http.StatusSeeOther)
}
