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

const dateLayout = "2006-01-02"

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
		d, err := time.ParseInLocation(dateLayout, fromStr, loc)
		if err != nil {
			if view.ErrorKey == "" {
				view.ErrorKey = "ReviewInvalidDate"
			}
		} else {
			fromDay = d
			fromOK = true
			filter.From = d
		}
	}

	if toStr != "" {
		d, err := time.ParseInLocation(dateLayout, toStr, loc)
		if err != nil {
			if view.ErrorKey == "" {
				view.ErrorKey = "ReviewInvalidDate"
			}
		} else {
			toDay = d
			toOK = true
			// Inclusive end date: [From 00:00, (To + 1 day) 00:00) in loc.
			filter.To = d.AddDate(0, 0, 1)
		}
	}

	if fromOK && toOK && toDay.Before(fromDay) {
		if view.ErrorKey == "" {
			view.ErrorKey = "ReviewInvalidDateRange"
		}
	}

	page := 1
	if pStr := strings.TrimSpace(q.Get("page")); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 1 {
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

