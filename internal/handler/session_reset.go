package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/pavelanni/examiner/internal/handler/views"
	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func sessionResetConflict(w http.ResponseWriter, r *http.Request, err error) bool {
	if !errors.Is(err, store.ErrSessionAnnulled) {
		return false
	}
	http.Error(w, appI18n.T(r.Context(), "AttemptResetConflict"), http.StatusConflict)
	return true
}

func (h *Handler) handleResetAttemptPage(w http.ResponseWriter, r *http.Request) {
	h.resetAttempt(w, r, false)
}

func (h *Handler) handleResetAttempt(w http.ResponseWriter, r *http.Request) {
	h.resetAttempt(w, r, true)
}

func (h *Handler) resetAttempt(w http.ResponseWriter, r *http.Request, submit bool) {
	sessionID, ok := parseIDParam(w, r, "sessionID")
	if !ok {
		return
	}
	view, err := h.store.GetSessionView(sessionID)
	if err != nil {
		lookupError(w, err, "failed to load attempt for reset", "session_id", sessionID)
		return
	}
	data := views.ResetAttemptData{View: *view}
	status := http.StatusOK
	if submit {
		if view.Session.Status == model.StatusAnnulled {
			sessionResetConflict(w, r, store.ErrSessionAnnulled)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, appI18n.T(r.Context(), "AttemptResetInvalidForm"), http.StatusBadRequest)
			return
		}
		data.Reason = strings.TrimSpace(r.PostForm.Get("reason"))
		if r.PostForm.Get("confirm") != "yes" {
			data.Error = appI18n.T(r.Context(), "AttemptResetConfirmRequired")
		} else {
			err := h.store.ResetSession(sessionID, model.UserFromContext(r.Context()).ID, data.Reason)
			switch {
			case err == nil:
				http.Redirect(w, r, h.path(fmt.Sprintf("/review/%d", sessionID)), http.StatusSeeOther)
				return
			case errors.Is(err, store.ErrSessionAnnulled):
				sessionResetConflict(w, r, err)
				return
			case errors.Is(err, store.ErrResetReasonTooLong):
				data.Error = appI18n.T(r.Context(), "AttemptResetReasonTooLong")
			case errors.Is(err, store.ErrResetForbidden):
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			default:
				lookupError(w, err, "failed to reset attempt", "session_id", sessionID)
				return
			}
		}
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := views.ResetAttemptPage(data).Render(r.Context(), w); err != nil {
		slog.Error("render reset attempt page", "error", err)
	}
}
