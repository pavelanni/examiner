package handler

import (
	"net/http"

	"github.com/pavelanni/examiner/internal/i18n"
)

// httpError writes the translation of msgID as a plain-text error. htmx
// shows the body to the user, so it is never a raw English string.
func httpError(w http.ResponseWriter, r *http.Request, status int, msgID string) {
	http.Error(w, i18n.T(r.Context(), msgID), status)
}
