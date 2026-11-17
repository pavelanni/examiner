package handler

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
)

// TestStatementRouteRoles checks exports through authentication and role middleware.
func TestStatementRouteRoles(t *testing.T) {
	env := newRBACEnv(t)
	if err := env.h.store.UpdateSessionStatus(env.sessGraded, model.StatusSubmitted); err != nil {
		t.Fatal(err)
	}
	if err := env.h.store.UpdateSessionStatus(env.sessGraded, model.StatusGraded); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"anon", "studentA", "studentB", "teacher", "admin"} {
		t.Run(role, func(t *testing.T) {
			want := http.StatusForbidden
			if role == "anon" {
				want = http.StatusSeeOther
			}
			if role == "teacher" || role == "admin" {
				want = http.StatusOK
			}
			rec := env.do(role, http.MethodGet, "/review/export?format=csv", nil)
			if rec.Code != want {
				t.Fatalf("status=%d, want=%d, body=%q", rec.Code, want, rec.Body.String())
			}
			if want == http.StatusOK {
				if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Header().Get("Content-Disposition"), ".csv") {
					t.Fatalf("missing export headers: %v", rec.Header())
				}
				reader := csv.NewReader(bytes.NewReader(rec.Body.Bytes()[3:]))
				reader.Comma = ';'
				rows, err := reader.ReadAll()
				if err != nil || len(rows) != 4 || rows[3][5] != "Не утверждена" {
					t.Fatalf("CSV = %v, %v", rows, err)
				}
				for _, name := range rows[2] {
					if strings.Contains(strings.ToLower(name), "id") {
						t.Fatalf("unexpected identifier column %q", name)
					}
				}
			}
		})
	}
}

// TestStatementBasePathAndFilters verifies the normal /ru review entry point.
func TestStatementBasePathAndFilters(t *testing.T) {
	env := newRBACEnv(t)
	env.h.config.BasePath = "/ru"
	router := chi.NewRouter()
	router.Use(i18n.Middleware("ru"))
	router.Route("/ru", func(r chi.Router) {
		r.Use(env.h.BasePathMiddleware)
		env.h.Routes(r)
	})
	env.router = router
	page := env.do("teacher", http.MethodGet, "/ru/review?topic=t&from=2026-11-01&to=2026-11-17&status=graded", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("review page = %d, %q", page.Code, page.Body.String())
	}
	for _, want := range []string{"/ru/review/export", `<option value="t" selected`, "2026-11-01", "2026-11-17", "Выгрузить ведомость"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Errorf("missing %q in review page", want)
		}
	}
	for _, path := range []string{
		"/ru/review/export?format=csv&status=invalid",
		"/ru/review/export?format=exe",
		"/ru/review/export?from=2026-11-20&to=2026-11-01",
		"/ru/review/export?timezone=invalid",
	} {
		rec := env.do("teacher", http.MethodGet, path, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d", path, rec.Code)
		}
	}
	rec := env.do("teacher", http.MethodGet, "/ru/review/export?format=xlsx", nil)
	if rec.Code != http.StatusOK || !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) {
		t.Fatalf("invalid XLSX response: %d", rec.Code)
	}
}

// TestStatementDateBounds checks Moscow midnight and an inclusive end date.
func TestStatementDateBounds(t *testing.T) {
	filter, _, err := statementFilter(url.Values{"from": {"2026-11-17"}, "to": {"2026-11-17"}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 16, 21, 0, 0, 0, time.UTC)
	if !filter.From.Equal(start) || !filter.Until.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("unexpected bounds: %+v", filter)
	}
}

// TestAdminCreateUserSavesGroup checks the same form used by administrators.
func TestAdminCreateUserSavesGroup(t *testing.T) {
	env := newRBACEnv(t)
	rec := env.do("admin", http.MethodPost, "/admin/users", url.Values{
		"username": {"newstudent"}, "password": {"password123"},
		"display_name": {"Тестовый студент"}, "group_name": {" Г-42 "}, "role": {"student"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create user = %d, %q", rec.Code, rec.Body.String())
	}
	user, err := env.h.store.GetUserByUsername("newstudent")
	if err != nil || user == nil || user.GroupName != "Г-42" {
		t.Fatalf("user = %+v, %v", user, err)
	}
	page := env.do("admin", http.MethodGet, "/admin/users", nil)
	if !strings.Contains(page.Body.String(), "Г-42") {
		t.Fatal("group is missing from the user list")
	}
}

// TestStatementClosedStoreDoesNotLeakError checks failure before download headers.
func TestStatementClosedStoreDoesNotLeakError(t *testing.T) {
	env := newRBACEnv(t)
	_ = env.h.store.Close()
	req := httptest.NewRequest(http.MethodGet, "/review/export?format=csv", nil)
	rec := httptest.NewRecorder()
	env.h.handleStatement(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "database") || rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("unexpected error response: %d, %q", rec.Code, rec.Body.String())
	}
}

// TestStatementTopicDropdown keeps historical topics and exact topic names
// available without requiring the teacher to type them.
func TestStatementTopicDropdown(t *testing.T) {
	env := newRBACEnv(t)
	for _, topic := range []string{"Механика", "Электричество"} {
		session, _, err := env.h.store.StartSession(1, 1, topic, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.h.store.UpdateSessionStatus(session.ID, model.StatusSubmitted); err != nil {
			t.Fatal(err)
		}
	}
	page := env.do("teacher", http.MethodGet, "/review?topic="+url.QueryEscape("Механика"), nil)
	if page.Code != http.StatusOK {
		t.Fatalf("status = %d", page.Code)
	}
	body := page.Body.String()
	for _, want := range []string{
		`<select name="topic">`, `<option value="">All topics</option>`,
		`<option value="Механика" selected>Механика</option>`,
		`<option value="Электричество">Электричество</option>`,
		`<option value="t">t</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in dropdown", want)
		}
	}
	if strings.Contains(body, `<input name="topic"`) {
		t.Fatal("topic still uses a free-text input")
	}
	if strings.Count(body, `value="Механика"`) != 1 {
		t.Fatal("topic appears more than once")
	}
	path := "/review/export?format=csv&topic=" + url.QueryEscape("Механика")
	rec := env.do("teacher", http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d", rec.Code)
	}
	reader := csv.NewReader(bytes.NewReader(rec.Body.Bytes()[3:]))
	reader.Comma = ';'
	rows, err := reader.ReadAll()
	if err != nil || len(rows) != 4 || rows[3][3] != "Механика" {
		t.Fatalf("filtered rows = %v, error = %v", rows, err)
	}
}
