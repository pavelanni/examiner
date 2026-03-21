package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/pavelanni/examiner/internal/grader/handler/views"
	"github.com/pavelanni/examiner/internal/model"
)

// dashboard renders the main dashboard listing all imported exams.
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	exams, err := h.store.ListExams()
	if err != nil {
		slog.Error("list exams", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.DashboardPage(exams).Render(r.Context(), w); err != nil {
		slog.Error("render dashboard", "error", err)
	}
}

// examStudentList renders the student list for a specific exam.
func (h *Handler) examStudentList(w http.ResponseWriter, r *http.Request) {
	examID := chi.URLParam(r, "examID")

	exam, err := h.store.GetExamByID(examID)
	if err != nil {
		slog.Error("get exam", "error", err, "examID", examID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if exam == nil {
		http.Error(w, "exam not found", http.StatusNotFound)
		return
	}

	students, err := h.store.ListStudentsForExam(examID)
	if err != nil {
		slog.Error("list students", "error", err, "examID", examID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.StudentsPage(examID, exam.Subject, students).Render(r.Context(), w); err != nil {
		slog.Error("render students page", "error", err)
	}
}

// reviewPage renders the review page for a specific student session.
func (h *Handler) reviewPage(w http.ResponseWriter, r *http.Request) {
	sessionID, err := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

	data, err := h.store.GetReviewData(sessionID)
	if err != nil {
		slog.Error("get review data", "error", err, "sessionID", sessionID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ReviewPage(*data).Render(r.Context(), w); err != nil {
		slog.Error("render review page", "error", err)
	}
}

// handleUpdateScore processes a teacher score update for a single question.
func (h *Handler) handleUpdateScore(w http.ResponseWriter, r *http.Request) {
	examID := chi.URLParam(r, "examID")
	sessionIDStr := chi.URLParam(r, "sessionID")
	questionID, err := strconv.ParseInt(chi.URLParam(r, "questionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid question ID", http.StatusBadRequest)
		return
	}
	sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	score, err := strconv.ParseFloat(r.FormValue("teacher_score"), 64)
	if err != nil {
		http.Error(w, "invalid score", http.StatusBadRequest)
		return
	}
	if score < 0 {
		http.Error(w, "score must be >= 0", http.StatusBadRequest)
		return
	}
	comment := r.FormValue("teacher_comment")

	if err := h.store.UpdateTeacherScore(questionID, score, comment); err != nil {
		slog.Error("update teacher score", "error", err, "questionID", questionID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Transition session from "imported" to "in_review" on first score.
	data, err := h.store.GetReviewData(sessionID)
	if err != nil {
		slog.Error("get review data after score update", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data != nil && data.Status == "imported" {
		if err := h.store.SetSessionStatus(sessionID, "in_review"); err != nil {
			slog.Error("set session status", "error", err, "sessionID", sessionID)
		}
	}

	// Find the updated question for the htmx partial response.
	if data != nil {
		for _, q := range data.Questions {
			if q.ID == questionID {
				successMsg := "Score saved"
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if err := views.ScoreForm(examID, sessionID, q, &successMsg).Render(r.Context(), w); err != nil {
					slog.Error("render score form", "error", err)
				}
				return
			}
		}
	}

	http.Error(w, "question not found after update", http.StatusInternalServerError)
}

// handleFinalize processes the final grade submission for a session.
func (h *Handler) handleFinalize(w http.ResponseWriter, r *http.Request) {
	examID := chi.URLParam(r, "examID")
	sessionID, err := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	finalGrade, err := strconv.ParseFloat(r.FormValue("final_grade"), 64)
	if err != nil {
		http.Error(w, "invalid grade", http.StatusBadRequest)
		return
	}
	if finalGrade < 0 || finalGrade > 100 {
		http.Error(w, "grade must be between 0 and 100", http.StatusBadRequest)
		return
	}
	teacherComment := r.FormValue("teacher_comment")

	user := model.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if err := h.store.FinalizeGrade(sessionID, finalGrade, teacherComment, user.ID); err != nil {
		slog.Error("finalize grade", "error", err, "sessionID", sessionID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/exam/%s", examID), http.StatusSeeOther)
}
