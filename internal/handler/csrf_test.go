package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
)

const csrfMessage = "Your session has expired"

func csrfPost(t *testing.T, cookie, form string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	body := url.Values{}
	if form != "" {
		body.Set("csrf_token", form)
	}
	req := httptest.NewRequest(http.MethodPost, "/exam/1/answer/1", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: cookie})
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	req = req.WithContext(i18n.WithLocalizer(req.Context(), i18n.NewLocalizer("en")))

	rec := httptest.NewRecorder()
	h.csrfMiddleware(next).ServeHTTP(rec, req)
	return rec
}

func TestCSRFFailureIsVisible(t *testing.T) {
	tests := []struct {
		name         string
		cookie, form string
	}{
		{"cookie missing", "", "tok"},
		{"form token missing", "tok", ""},
		{"mismatch", "tok", "bad"},
	}
	for _, tt := range tests {
		for _, htmx := range []bool{false, true} {
			name := tt.name + "/page"
			if htmx {
				name = tt.name + "/htmx"
			}
			t.Run(name, func(t *testing.T) {
				rec := csrfPost(t, tt.cookie, tt.form, htmx)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status %d, want 403", rec.Code)
				}
				if !strings.Contains(rec.Body.String(), csrfMessage) {
					t.Errorf("body %q does not contain the user-facing message", rec.Body.String())
				}
			})
		}
	}
}

func TestCSRFValidTokenPasses(t *testing.T) {
	if rec := csrfPost(t, "tok", "tok", false); rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
}
