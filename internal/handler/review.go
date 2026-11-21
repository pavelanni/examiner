package handler

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pavelanni/examiner/internal/handler/views"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

const reviewPageSize = 50

const maxReviewPage = 1000000

const dateLayout = "2006-01-02"

func parseDateParts(s string) (year int, month time.Month, day int, err error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return 0, 0, 0, err
	}
	y, m, d := t.Date()
	return y, m, d, nil
}

// firstInstantOfDay returns the first instant of the given calendar day in loc,
// accounting for time zones that skip midnight (e.g. DST transitions).
func firstInstantOfDay(y int, m time.Month, d int, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	t := time.Date(y, m, d, 0, 0, 0, 0, loc)
	if t.Year() != y || t.Month() != m || t.Day() != d {
		// Midnight was skipped in this location due to DST gap.
		_, end := t.ZoneBounds()
		if !end.IsZero() && end.In(loc).Year() == y && end.In(loc).Month() == m && end.In(loc).Day() == d {
			return end.In(loc)
		}
		for hr := 1; hr <= 4; hr++ {
			cand := time.Date(y, m, d, hr, 0, 0, 0, loc)
			if cand.Year() == y && cand.Month() == m && cand.Day() == d {
				return cand
			}
		}
	}
	return t
}

func (h *Handler) reviewLocation() *time.Location {
	if h.config.Location != nil {
		return h.config.Location
	}
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.UTC
	}
	return loc
}

func (h *Handler) now() time.Time {
	if h.nowFunc != nil {
		return h.nowFunc()
	}
	return time.Now()
}

// parseReviewFilter parses query parameters from GET /review into a store.ReviewFilter
// and the form/pagination state for views.ReviewListView.
func parseReviewFilter(q url.Values, loc *time.Location, now time.Time) (store.ReviewFilter, views.ReviewListView) {
	if loc == nil {
		loc = time.UTC
	}

	view := views.ReviewListView{
		Status:   "all",
		Page:     1,
		Location: loc,
	}
	var filter store.ReviewFilter

	status := strings.TrimSpace(q.Get("status"))
	switch status {
	case "", "all":
		view.Status = "all"
	case string(model.StatusGraded):
		view.Status = string(model.StatusGraded)
		filter.Statuses = []model.SessionStatus{model.StatusGraded}
	case string(model.StatusReviewed):
		view.Status = string(model.StatusReviewed)
		filter.Statuses = []model.SessionStatus{model.StatusReviewed}
	case string(model.StatusGradingFailed):
		view.Status = string(model.StatusGradingFailed)
		filter.Statuses = []model.SessionStatus{model.StatusGradingFailed}
	default:
		view.Status = status
		view.ErrorKey = "ReviewInvalidStatus"
	}

	preset := strings.TrimSpace(q.Get("preset"))
	fromStr := strings.TrimSpace(q.Get("from"))
	toStr := strings.TrimSpace(q.Get("to"))
	dateStr := strings.TrimSpace(q.Get("date"))

	switch preset {
	case "today":
		today := now.In(loc).Format(dateLayout)
		fromStr, toStr = today, today
	case "all":
		fromStr, toStr = "", ""
	default:
		if fromStr == "" && toStr == "" && dateStr != "" {
			fromStr, toStr = dateStr, dateStr
		}
	}

	view.From = fromStr
	view.To = toStr

	var fromDay, toDay time.Time
	var fromOK, toOK bool

	if fromStr != "" {
		y, m, d, err := parseDateParts(fromStr)
		if err != nil {
			if view.ErrorKey == "" {
				view.ErrorKey = "ReviewInvalidDate"
			}
		} else {
			fromDay = firstInstantOfDay(y, m, d, loc)
			fromOK = true
			filter.From = fromDay
		}
	}

	if toStr != "" {
		y, m, d, err := parseDateParts(toStr)
		if err != nil {
			if view.ErrorKey == "" {
				view.ErrorKey = "ReviewInvalidDate"
			}
		} else {
			toDay = firstInstantOfDay(y, m, d, loc)
			toOK = true
			// Inclusive end date: [From, NextDayStart) in loc.
			nextDate := time.Date(y, m, d+1, 12, 0, 0, 0, time.UTC)
			ny, nm, nd := nextDate.Date()
			filter.To = firstInstantOfDay(ny, nm, nd, loc)
		}
	}

	if fromOK && toOK && toDay.Before(fromDay) {
		if view.ErrorKey == "" {
			view.ErrorKey = "ReviewInvalidDateRange"
		}
	}

	page := 1
	if pStr := strings.TrimSpace(q.Get("page")); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 1 && p <= maxReviewPage {
			page = p
		}
	}
	view.Page = page
	filter.Limit = reviewPageSize
	filter.Offset = (page - 1) * reviewPageSize

	return filter, view
}

func buildReviewPageURL(status, from, to string, page int) string {
	vals := url.Values{}
	if status != "" && status != "all" {
		vals.Set("status", status)
	}
	if from != "" {
		vals.Set("from", from)
	}
	if to != "" {
		vals.Set("to", to)
	}
	if page > 1 {
		vals.Set("page", strconv.Itoa(page))
	}
	encoded := vals.Encode()
	if encoded == "" {
		return "/review"
	}
	return "/review?" + encoded
}

