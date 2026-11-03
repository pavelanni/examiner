package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestParseReviewFilter(t *testing.T) {
	msk := mustLocation(t, "Europe/Moscow")
	// 2026-11-03 21:30 UTC is 2026-11-04 00:30 in Europe/Moscow.
	now := time.Date(2026, 11, 3, 21, 30, 0, 0, time.UTC)

	day := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, msk)
	}

	t.Run("defaults to all statuses and page 1", func(t *testing.T) {
		f, v := parseReviewFilter(url.Values{}, msk, now)
		if len(f.Statuses) != 0 {
			t.Errorf("Statuses = %v, want empty (all reviewable)", f.Statuses)
		}
		if v.Status != "all" || v.From != "" || v.To != "" || v.Page != 1 || v.ErrorKey != "" {
			t.Errorf("view = %+v", v)
		}
		if f.Limit != reviewPageSize || f.Offset != 0 {
			t.Errorf("limit=%d offset=%d", f.Limit, f.Offset)
		}
	})

	t.Run("status graded and reviewed", func(t *testing.T) {
		f, v := parseReviewFilter(url.Values{"status": {"graded"}}, msk, now)
		if len(f.Statuses) != 1 || f.Statuses[0] != model.StatusGraded || v.Status != "graded" {
			t.Errorf("got statuses=%v view.Status=%q", f.Statuses, v.Status)
		}

		f, v = parseReviewFilter(url.Values{"status": {"reviewed"}}, msk, now)
		if len(f.Statuses) != 1 || f.Statuses[0] != model.StatusReviewed || v.Status != "reviewed" {
			t.Errorf("got statuses=%v view.Status=%q", f.Statuses, v.Status)
		}
	})

	t.Run("invalid status sets ErrorKey", func(t *testing.T) {
		_, v := parseReviewFilter(url.Values{"status": {"in_progress"}}, msk, now)
		if v.ErrorKey != "ReviewInvalidStatus" {
			t.Errorf("ErrorKey = %q, want ReviewInvalidStatus", v.ErrorKey)
		}
	})

	t.Run("single date parameter", func(t *testing.T) {
		f, v := parseReviewFilter(url.Values{"date": {"2026-11-03"}}, msk, now)
		if v.From != "2026-11-03" || v.To != "2026-11-03" || v.ErrorKey != "" {
			t.Errorf("view = %+v", v)
		}
		if !f.From.Equal(day(2026, 11, 3)) || !f.To.Equal(day(2026, 11, 4)) {
			t.Errorf("range = [%v, %v)", f.From, f.To)
		}
	})

	t.Run("preset today uses configured location", func(t *testing.T) {
		q := url.Values{
			"from":   {"2026-10-01"},
			"to":     {"2026-10-02"},
			"preset": {"today"},
		}
		f, v := parseReviewFilter(q, msk, now)
		// In Moscow, now is already 2026-11-04.
		if v.From != "2026-11-04" || v.To != "2026-11-04" {
			t.Errorf("From=%q To=%q, want 2026-11-04", v.From, v.To)
		}
		if !f.From.Equal(day(2026, 11, 4)) || !f.To.Equal(day(2026, 11, 5)) {
			t.Errorf("range = [%v, %v)", f.From, f.To)
		}
	})

	t.Run("preset all clears dates", func(t *testing.T) {
		q := url.Values{
			"status": {"graded"},
			"from":   {"2026-11-01"},
			"to":     {"2026-11-03"},
			"preset": {"all"},
		}
		f, v := parseReviewFilter(q, msk, now)
		if v.From != "" || v.To != "" || !f.From.IsZero() || !f.To.IsZero() {
			t.Errorf("expected cleared dates, got view=%+v filter=%+v", v, f)
		}
		if v.Status != "graded" {
			t.Errorf("status = %q, want graded preserved", v.Status)
		}
	})

	t.Run("invalid date and inverted range", func(t *testing.T) {
		_, v := parseReviewFilter(url.Values{"from": {"not-a-date"}}, msk, now)
		if v.ErrorKey != "ReviewInvalidDate" {
			t.Errorf("ErrorKey = %q, want ReviewInvalidDate", v.ErrorKey)
		}

		_, v = parseReviewFilter(url.Values{"from": {"2026-11-05"}, "to": {"2026-11-03"}}, msk, now)
		if v.ErrorKey != "ReviewInvalidDateRange" {
			t.Errorf("ErrorKey = %q, want ReviewInvalidDateRange", v.ErrorKey)
		}
	})

	t.Run("pagination", func(t *testing.T) {
		f, v := parseReviewFilter(url.Values{"page": {"3"}}, msk, now)
		if v.Page != 3 || f.Offset != 2*reviewPageSize || f.Limit != reviewPageSize {
			t.Errorf("page=%d offset=%d limit=%d", v.Page, f.Offset, f.Limit)
		}

		f, v = parseReviewFilter(url.Values{"page": {"-2"}}, msk, now)
		if v.Page != 1 || f.Offset != 0 {
			t.Errorf("negative page: page=%d offset=%d", v.Page, f.Offset)
		}
	})
}

type reviewTestEnv struct {
	t      *testing.T
	store  *store.Store
	h      *Handler
	router *chi.Mux
	bpID   int64
}

func newReviewTestEnv(t *testing.T, lang, basePath string) *reviewTestEnv {
	t.Helper()
	if err := i18n.Init(lang); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bpID, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Physics", MaxFollowups: 2})
	if err != nil {
		t.Fatalf("CreateBlueprint: %v", err)
	}

	msk := mustLocation(t, "Europe/Moscow")
	h := &Handler{
		store: s,
		config: model.ExamConfig{
			BasePath: basePath,
			Location: msk,
		},
		nowFunc: func() time.Time {
			return time.Date(2026, 11, 3, 21, 30, 0, 0, time.UTC) // 2026-11-04 00:30 MSK
		},
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			teacher := &model.User{
				ID:          99,
				Username:    "reviewer_login",
				DisplayName: "Reviewer Teacher",
				Role:        model.UserRoleTeacher,
				Active:      true,
			}
			ctx := model.ContextWithUser(req.Context(), teacher)
			ctx = model.ContextWithBasePath(ctx, basePath)
			ctx = i18n.WithLocalizer(ctx, i18n.NewLocalizer(lang))
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	if basePath != "" {
		r.Route(basePath, func(sub chi.Router) {
			sub.Get("/review", h.handleReviewList)
			sub.Get("/review/{sessionID}", h.handleReviewPage)
		})
	} else {
		r.Get("/review", h.handleReviewList)
		r.Get("/review/{sessionID}", h.handleReviewPage)
	}

	return &reviewTestEnv{t: t, store: s, h: h, router: r, bpID: bpID}
}

func (e *reviewTestEnv) addSession(studentID int64, status model.SessionStatus, started time.Time, llmGrade float64, finalGrade *float64) int64 {
	e.t.Helper()
	id, err := e.store.CreateSession(e.bpID, studentID, nil)
	if err != nil {
		e.t.Fatalf("CreateSession: %v", err)
	}
	submitted := started.Add(15 * time.Minute).UTC()
	if err := e.store.UpsertGrade(model.Grade{SessionID: id, LLMGrade: llmGrade}); err != nil {
		e.t.Fatalf("UpsertGrade: %v", err)
	}
	if finalGrade != nil {
		if err := e.store.FinalizeGrade(id, *finalGrade, 99); err != nil {
			e.t.Fatalf("FinalizeGrade: %v", err)
		}
	}
	// Set status and timestamps deterministically via UpdateSessionStatus + direct started_at via store helper:
	if err := e.store.SetSessionTimestampsForTest(id, status, started.UTC(), &submitted); err != nil {
		e.t.Fatalf("SetSessionTimestampsForTest: %v", err)
	}
	return id
}

func (e *reviewTestEnv) get(path string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func TestHandleReviewListFiltersAndAnonymity(t *testing.T) {
	env := newReviewTestEnv(t, "ru", "/ru")
	msk := mustLocation(t, "Europe/Moscow")

	// Create a student with identifiable PII that must never appear on /review or /review/{id}.
	studentID, err := env.store.CreateUser(model.User{
		Username:     "secret_student_login",
		ExternalID:   "EXT-SECRET-777",
		DisplayName:  "Иванов Иван Секретович",
		PasswordHash: "hash",
		Role:         model.UserRoleStudent,
		Active:       true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	final92 := 92.5
	sNov2 := env.addSession(studentID, model.StatusReviewed, time.Date(2026, 11, 2, 14, 0, 0, 0, msk), 80.0, &final92)
	sNov3Night := env.addSession(studentID, model.StatusGraded, time.Date(2026, 11, 3, 23, 30, 0, 0, msk), 75.5, nil)

	// 1. Default view (all statuses): shows both sessions, newest first, with base-path /ru.
	rec := env.get("/ru/review")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ru/review: status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/ru/review"`) {
		t.Errorf("expected form action with /ru base-path, got:\n%s", body)
	}
	idxNov3 := strings.Index(body, fmt.Sprintf("/ru/review/%d", sNov3Night))
	idxNov2 := strings.Index(body, fmt.Sprintf("/ru/review/%d", sNov2))
	if idxNov3 == -1 || idxNov2 == -1 || idxNov3 > idxNov2 {
		t.Errorf("expected session %d before %d in body", sNov3Night, sNov2)
	}
	if !strings.Contains(body, "2026-11-03 23:30") {
		t.Errorf("expected Moscow time 2026-11-03 23:30 in body")
	}
	if !strings.Contains(body, "75.5%") || !strings.Contains(body, "92.5%") {
		t.Errorf("expected LLM and final grades in body")
	}

	// Anonymity check on list page and detail page.
	for _, secret := range []string{"secret_student_login", "EXT-SECRET-777", "Иванов Иван Секретович"} {
		if strings.Contains(body, secret) {
			t.Errorf("review list leaked student identity %q", secret)
		}
	}
	detailRec := env.get(fmt.Sprintf("/ru/review/%d", sNov3Night))
	if detailRec.Code != http.StatusOK {
		t.Fatalf("GET detail: status %d", detailRec.Code)
	}
	detailBody := detailRec.Body.String()
	for _, secret := range []string{"secret_student_login", "EXT-SECRET-777", "Иванов Иван Секретович"} {
		if strings.Contains(detailBody, secret) {
			t.Errorf("review detail leaked student identity %q", secret)
		}
	}

	// 2. Filter by status=graded: only sNov3Night appears.
	rec = env.get("/ru/review?status=graded")
	body = rec.Body.String()
	if !strings.Contains(body, fmt.Sprintf("/ru/review/%d", sNov3Night)) {
		t.Errorf("expected graded session %d", sNov3Night)
	}
	if strings.Contains(body, fmt.Sprintf("/ru/review/%d", sNov2)) {
		t.Errorf("did not expect reviewed session %d when status=graded", sNov2)
	}

	// 3. Filter by Moscow date 2026-11-03: sNov3Night (23:30 MSK = 20:30 UTC) matches, sNov2 does not.
	rec = env.get("/ru/review?from=2026-11-03&to=2026-11-03")
	body = rec.Body.String()
	if !strings.Contains(body, fmt.Sprintf("/ru/review/%d", sNov3Night)) || strings.Contains(body, fmt.Sprintf("/ru/review/%d", sNov2)) {
		t.Errorf("date filter 2026-11-03 failed:\n%s", body)
	}
	if !strings.Contains(body, `value="2026-11-03"`) {
		t.Errorf("expected date input to keep value 2026-11-03")
	}

	// 4. Invalid date range returns HTTP 200 and localized error message.
	rec = env.get("/ru/review?from=2026-11-05&to=2026-11-03")
	if rec.Code != http.StatusOK {
		t.Fatalf("invalid range status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Начальная дата не может быть позже конечной.") {
		t.Errorf("expected localized error message in body:\n%s", rec.Body.String())
	}
}

func TestHandleReviewListPagination(t *testing.T) {
	env := newReviewTestEnv(t, "en", "")
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for i := range 55 {
		env.addSession(1, model.StatusGraded, base.Add(time.Duration(i)*time.Minute), 50, nil)
	}

	rec1 := env.get("/review?status=graded")
	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1 status = %d", rec1.Code)
	}
	body1 := rec1.Body.String()
	if !strings.Contains(body1, "Page 1 of 2 (55 total)") {
		t.Errorf("expected pagination summary on page 1, got:\n%s", body1)
	}
	if !strings.Contains(body1, `/review?page=2&amp;status=graded`) && !strings.Contains(body1, `/review?status=graded&amp;page=2`) {
		t.Errorf("expected next page link preserving status=graded, got:\n%s", body1)
	}

	rec2 := env.get("/review?status=graded&page=2")
	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d", rec2.Code)
	}
	body2 := rec2.Body.String()
	if !strings.Contains(body2, "Page 2 of 2 (55 total)") {
		t.Errorf("expected pagination summary on page 2")
	}
	if strings.Count(body2, `href="/review/`) < 5 {
		t.Errorf("expected 5 session links on page 2")
	}
}
