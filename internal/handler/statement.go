package handler

import (
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/statement"
)

// statementFilter validates the URL filters. Date boundaries use exam start time
// to match Teacher Review (#52), while the exported date is submitted_at.
func statementFilter(query url.Values) (model.StatementFilter, *time.Location, error) {
	var filter model.StatementFilter
	zone := query.Get("timezone")
	if zone == "" {
		zone = "Europe/Moscow"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return filter, nil, fmt.Errorf("invalid timezone")
	}
	if query.Get("topic") != "" {
		value := query.Get("topic")
		filter.Topic = &value
	}
	filter.Status = model.SessionStatus(query.Get("status"))
	switch filter.Status {
	case "", "all":
		filter.Status = ""
	case model.StatusSubmitted, model.StatusGrading, model.StatusGradingFailed, model.StatusGraded, model.StatusReviewed:
	default:
		return filter, nil, fmt.Errorf("invalid status")
	}
	from, to := query.Get("from"), query.Get("to")
	if date := query.Get("date"); date != "" {
		from, to = date, date
	}
	if from != "" {
		t, err := time.ParseInLocation("2006-01-02", from, location)
		if err != nil {
			return filter, nil, fmt.Errorf("invalid start date")
		}
		filter.From = &t
	}
	if to != "" {
		t, err := time.ParseInLocation("2006-01-02", to, location)
		if err != nil {
			return filter, nil, fmt.Errorf("invalid end date")
		}
		t = t.AddDate(0, 0, 1)
		filter.Until = &t
	}
	if filter.From != nil && filter.Until != nil && !filter.From.Before(*filter.Until) {
		return filter, nil, fmt.Errorf("start date is after end date")
	}
	return filter, location, nil
}

// statementFilename keeps user-supplied topics out of paths and response headers.
func statementFilename(topic string, generated time.Time, format string) string {
	var name strings.Builder
	for _, r := range topic {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			name.WriteRune(r)
		} else {
			name.WriteByte('_')
		}
		if name.Len() >= 100 {
			break
		}
	}
	if name.Len() == 0 {
		name.WriteString("all")
	}
	return "vedomost_" + name.String() + "_" + generated.Format("2006-01-02") + "." + format
}

// handleStatement exports submitted attempts to a teacher or administrator.
func (h *Handler) handleStatement(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "xlsx" {
		http.Error(w, "invalid export format", http.StatusBadRequest)
		return
	}
	filter, location, err := statementFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	report, err := h.store.ExamStatement(filter)
	if err != nil {
		slog.Error("failed to query exam statement", "error", err)
		http.Error(w, "failed to export statement", http.StatusInternalServerError)
		return
	}
	generated := time.Now().In(location)
	var data []byte
	contentType := "text/csv; charset=utf-8"
	if format == "xlsx" {
		data, err = statement.XLSX(report, generated, location)
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	} else {
		data, err = statement.CSV(report, generated, location)
	}
	if err != nil {
		slog.Error("failed to render exam statement", "error", err)
		http.Error(w, "failed to export statement", http.StatusInternalServerError)
		return
	}
	topic := "all"
	if filter.Topic != nil {
		topic = *filter.Topic
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": statementFilename(topic, generated, format)}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(data); err != nil {
		slog.Error("failed to send exam statement", "error", err)
		return
	}
	user := model.UserFromContext(r.Context())
	slog.Info("exported exam statement", "user_id", user.ID, "username", user.Username,
		"generated_at", generated, "format", format, "rows", len(report.Rows))
}
