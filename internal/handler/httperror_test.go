package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
)

func TestHTTPErrorLocalized(t *testing.T) {
	if err := i18n.Init("en"); err != nil {
		t.Fatal(err)
	}
	for lang, want := range map[string]string{"ru": "Недостаточно прав.\n", "en": "forbidden\n"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(i18n.WithLocalizer(req.Context(), i18n.NewLocalizer(lang)))
		rec := httptest.NewRecorder()
		httpError(rec, req, http.StatusForbidden, "ErrForbidden")
		if rec.Code != http.StatusForbidden || rec.Body.String() != want {
			t.Errorf("%s: %d %q, want 403 %q", lang, rec.Code, rec.Body.String(), want)
		}
	}
}
