package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/pavelanni/examiner/internal/grader/report"
)

func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	sessionID, err := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}

	data, err := h.store.GetReviewData(sessionID)
	if err != nil {
		slog.Error("get review data for report", "error", err, "sessionID", sessionID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	md := report.Generate(*data)
	filename := fmt.Sprintf("%s-%s-report.md", data.ExamID, data.ExternalID)

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write([]byte(md))
}
