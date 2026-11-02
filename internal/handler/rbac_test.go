package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// rbacEnv is the full router with real auth sessions: two students, a
// teacher and an admin. Student A owns sessA (in progress) and sessGraded.
type rbacEnv struct {
	h      *Handler
	store  *store.Store
	router http.Handler
	tokens map[string]string // role name -> auth session token; "anon" has none

	sessA, threadA           int64 // in progress
	sessGraded, threadGraded int64 // graded, awaiting review
	sessOther, threadOther   int64 // graded, owned by student B
}

const csrfTestToken = "test-csrf-token"

func newRBACEnv(t *testing.T) *rbacEnv {
	t.Helper()
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	env := &rbacEnv{store: s, tokens: map[string]string{}}
	userIDs := map[string]int64{}
	for _, u := range []struct {
		name string
		role model.UserRole
	}{
		{"studentA", model.UserRoleStudent},
		{"studentB", model.UserRoleStudent},
		{"teacher", model.UserRoleTeacher},
		{"admin", model.UserRoleAdmin},
	} {
		id, err := s.CreateUser(model.User{Username: u.name, DisplayName: u.name, PasswordHash: "x", Role: u.role, Active: true})
		if err != nil {
			t.Fatalf("CreateUser %s: %v", u.name, err)
		}
		tok, err := s.CreateAuthSession(id)
		if err != nil {
			t.Fatalf("CreateAuthSession %s: %v", u.name, err)
		}
		userIDs[u.name] = id
		env.tokens[u.name] = tok
	}

	bpID, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "T", MaxFollowups: 3})
	if err != nil {
		t.Fatalf("CreateBlueprint: %v", err)
	}
	qID, err := s.InsertQuestion(model.Question{
		CourseID: 1, Text: "Q1", Difficulty: model.Difficulty("easy"),
		Topic: "t", Rubric: "r", ModelAnswer: "a", MaxPoints: 10,
	})
	if err != nil {
		t.Fatalf("InsertQuestion: %v", err)
	}
	newSession := func(student int64, graded bool) (int64, int64) {
		sessID, err := s.CreateSession(bpID, student, []int64{qID})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		threads, err := s.GetThreadsForSession(sessID)
		if err != nil || len(threads) != 1 {
			t.Fatalf("GetThreadsForSession: %v (%d threads)", err, len(threads))
		}
		if graded {
			if err := s.UpsertScore(model.QuestionScore{ThreadID: threads[0].ID, LLMScore: 5}); err != nil {
				t.Fatalf("UpsertScore: %v", err)
			}
			if err := s.UpsertGrade(model.Grade{SessionID: sessID, LLMGrade: 50}); err != nil {
				t.Fatalf("UpsertGrade: %v", err)
			}
			if err := s.UpdateSessionStatus(sessID, model.StatusGraded); err != nil {
				t.Fatalf("UpdateSessionStatus: %v", err)
			}
		}
		return sessID, threads[0].ID
	}
	env.sessA, env.threadA = newSession(userIDs["studentA"], false)
	env.sessGraded, env.threadGraded = newSession(userIDs["studentA"], true)
	env.sessOther, env.threadOther = newSession(userIDs["studentB"], true)

	env.h = &Handler{store: s, llm: &fakeLLM{}}
	r := chi.NewRouter()
	r.Use(i18n.Middleware("en"))
	env.h.Routes(r)
	env.router = r
	return env
}

// do sends a request as the given role ("anon" for no session). POST
// requests carry a valid CSRF token so only authorization is under test.
func (e *rbacEnv) do(role, method, path string, form url.Values) *httptest.ResponseRecorder {
	if form == nil {
		form = url.Values{}
	}
	var req *http.Request
	if method == http.MethodPost {
		form.Set("csrf_token", csrfTestToken)
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfTestToken})
	if tok, ok := e.tokens[role]; ok {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func TestRouteRoleMatrix(t *testing.T) {
	env := newRBACEnv(t)
	exam := fmt.Sprintf("/exam/%d", env.sessA)
	results := fmt.Sprintf("/results/%d", env.sessA)
	review := fmt.Sprintf("/review/%d", env.sessGraded)

	const login = http.StatusSeeOther // anonymous requests redirect to /login
	tests := []struct {
		path string
		want map[string]int // role -> status
	}{
		{"/", map[string]int{"anon": login, "studentA": 200, "studentB": 200, "teacher": 200, "admin": 200}},
		{exam, map[string]int{"anon": login, "studentA": 200, "studentB": 403, "teacher": 200, "admin": 200}},
		// Results are the student's own page; staff use /review instead.
		{results, map[string]int{"anon": login, "studentA": 200, "studentB": 403, "teacher": 403, "admin": 403}},
		{"/review", map[string]int{"anon": login, "studentA": 403, "studentB": 403, "teacher": 200, "admin": 200}},
		{review, map[string]int{"anon": login, "studentA": 403, "studentB": 403, "teacher": 200, "admin": 200}},
		{"/admin/users", map[string]int{"anon": login, "studentA": 403, "studentB": 403, "teacher": 403, "admin": 200}},
		{"/admin/questions", map[string]int{"anon": login, "studentA": 403, "studentB": 403, "teacher": 403, "admin": 200}},
	}
	for _, tt := range tests {
		for _, role := range []string{"anon", "studentA", "studentB", "teacher", "admin"} {
			t.Run(role+" GET "+tt.path, func(t *testing.T) {
				rec := env.do(role, http.MethodGet, tt.path, nil)
				if rec.Code != tt.want[role] {
					t.Fatalf("status %d, want %d", rec.Code, tt.want[role])
				}
				if role == "anon" && rec.Header().Get("Location") != "/login" {
					t.Errorf("Location = %q, want /login", rec.Header().Get("Location"))
				}
			})
		}
	}
}

func TestStudentCannotWriteToAnotherStudentsSession(t *testing.T) {
	env := newRBACEnv(t)

	answer := env.do("studentB", http.MethodPost,
		fmt.Sprintf("/exam/%d/answer/%d", env.sessA, env.threadA), url.Values{"answer": {"hijack"}})
	if answer.Code != http.StatusForbidden {
		t.Errorf("answer: status %d, want 403", answer.Code)
	}
	submit := env.do("studentB", http.MethodPost, fmt.Sprintf("/exam/%d/submit", env.sessA), nil)
	if submit.Code != http.StatusForbidden {
		t.Errorf("submit: status %d, want 403", submit.Code)
	}

	msgs, err := env.store.GetMessages(env.threadA)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("thread has %d messages, want 0", len(msgs))
	}
	sess, err := env.store.GetSession(env.sessA)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != model.StatusInProgress {
		t.Errorf("session status %q, want in_progress", sess.Status)
	}
}

func TestStudentCannotReachReviewWrites(t *testing.T) {
	env := newRBACEnv(t)
	score := env.do("studentA", http.MethodPost,
		fmt.Sprintf("/review/%d/score/%d", env.sessGraded, env.threadGraded), url.Values{"teacher_score": {"10"}})
	finalize := env.do("studentA", http.MethodPost,
		fmt.Sprintf("/review/%d/finalize", env.sessGraded), url.Values{"final_grade": {"100"}})
	if score.Code != http.StatusForbidden || finalize.Code != http.StatusForbidden {
		t.Fatalf("score %d, finalize %d, want 403 for both", score.Code, finalize.Code)
	}
	sc, err := env.store.GetScore(env.threadGraded)
	if err != nil {
		t.Fatal(err)
	}
	if sc.TeacherScore != nil {
		t.Errorf("teacher score changed to %v", *sc.TeacherScore)
	}
}

func TestMalformedAndMissingIDs(t *testing.T) {
	env := newRBACEnv(t)
	tests := []struct {
		role, method, path string
		want               int
	}{
		{"studentA", http.MethodGet, "/exam/abc", http.StatusBadRequest},
		{"studentA", http.MethodGet, "/exam/0", http.StatusBadRequest},
		{"studentA", http.MethodGet, "/exam/99999", http.StatusNotFound},
		{"studentA", http.MethodGet, "/results/99999", http.StatusNotFound},
		{"studentA", http.MethodPost, "/exam/abc/submit", http.StatusBadRequest},
		{"studentA", http.MethodPost, "/exam/99999/submit", http.StatusNotFound},
		{"studentA", http.MethodPost, fmt.Sprintf("/exam/%d/answer/xyz", env.sessA), http.StatusBadRequest},
		{"studentA", http.MethodPost, fmt.Sprintf("/exam/%d/answer/99999", env.sessA), http.StatusNotFound},
		{"teacher", http.MethodGet, "/review/abc", http.StatusBadRequest},
		{"teacher", http.MethodGet, "/review/99999", http.StatusNotFound},
		{"teacher", http.MethodPost, "/review/99999/finalize", http.StatusNotFound},
		{"teacher", http.MethodPost, fmt.Sprintf("/review/%d/score/99999", env.sessGraded), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.role+" "+tt.method+" "+tt.path, func(t *testing.T) {
			form := url.Values{"answer": {"a"}, "teacher_score": {"1"}, "final_grade": {"50"}}
			rec := env.do(tt.role, tt.method, tt.path, form)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (body %q)", rec.Code, tt.want, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "sql:") {
				t.Errorf("body leaks a database error: %q", rec.Body.String())
			}
		})
	}
}

func TestUpdateScoreValidation(t *testing.T) {
	scorePath := func(sess, thread int64) string { return fmt.Sprintf("/review/%d/score/%d", sess, thread) }
	tests := []struct {
		name  string
		path  func(e *rbacEnv) string
		score string
		want  int
	}{
		{"valid", func(e *rbacEnv) string { return scorePath(e.sessGraded, e.threadGraded) }, "7.5", http.StatusSeeOther},
		{"max points", func(e *rbacEnv) string { return scorePath(e.sessGraded, e.threadGraded) }, "10", http.StatusSeeOther},
		{"above max", func(e *rbacEnv) string { return scorePath(e.sessGraded, e.threadGraded) }, "11", http.StatusBadRequest},
		{"negative", func(e *rbacEnv) string { return scorePath(e.sessGraded, e.threadGraded) }, "-1", http.StatusBadRequest},
		{"NaN", func(e *rbacEnv) string { return scorePath(e.sessGraded, e.threadGraded) }, "NaN", http.StatusBadRequest},
		{"not a number", func(e *rbacEnv) string { return scorePath(e.sessGraded, e.threadGraded) }, "ten", http.StatusBadRequest},
		{"thread of another session", func(e *rbacEnv) string { return scorePath(e.sessGraded, e.threadOther) }, "5", http.StatusForbidden},
		{"session not graded", func(e *rbacEnv) string { return scorePath(e.sessA, e.threadA) }, "5", http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newRBACEnv(t)
			rec := env.do("teacher", http.MethodPost, tt.path(env), url.Values{"teacher_score": {tt.score}})
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (body %q)", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == http.StatusSeeOther {
				return
			}
			// A rejected update leaves every score untouched.
			for _, thread := range []int64{env.threadGraded, env.threadOther} {
				sc, err := env.store.GetScore(thread)
				if err != nil {
					t.Fatal(err)
				}
				if sc != nil && sc.TeacherScore != nil {
					t.Errorf("thread %d teacher score changed to %v", thread, *sc.TeacherScore)
				}
			}
		})
	}
}

func TestScoreUpdateRejectedAfterReview(t *testing.T) {
	env := newRBACEnv(t)
	if rec := env.do("teacher", http.MethodPost, fmt.Sprintf("/review/%d/finalize", env.sessGraded),
		url.Values{"final_grade": {"60"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("finalize: status %d", rec.Code)
	}
	rec := env.do("teacher", http.MethodPost, fmt.Sprintf("/review/%d/score/%d", env.sessGraded, env.threadGraded),
		url.Values{"teacher_score": {"9"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("score after review: status %d, want 409", rec.Code)
	}
}

func TestFinalizeValidation(t *testing.T) {
	tests := []struct {
		name  string
		sess  func(e *rbacEnv) int64
		grade string
		want  int
	}{
		{"valid", func(e *rbacEnv) int64 { return e.sessGraded }, "80", http.StatusSeeOther},
		{"above 100", func(e *rbacEnv) int64 { return e.sessGraded }, "101", http.StatusBadRequest},
		{"negative", func(e *rbacEnv) int64 { return e.sessGraded }, "-5", http.StatusBadRequest},
		{"NaN", func(e *rbacEnv) int64 { return e.sessGraded }, "NaN", http.StatusBadRequest},
		{"in progress", func(e *rbacEnv) int64 { return e.sessA }, "80", http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newRBACEnv(t)
			rec := env.do("teacher", http.MethodPost, fmt.Sprintf("/review/%d/finalize", tt.sess(env)),
				url.Values{"final_grade": {tt.grade}})
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (body %q)", rec.Code, tt.want, rec.Body.String())
			}
			sess, err := env.store.GetSession(tt.sess(env))
			if err != nil {
				t.Fatal(err)
			}
			if wantReviewed := tt.want == http.StatusSeeOther; (sess.Status == model.StatusReviewed) != wantReviewed {
				t.Errorf("session status %q after finalize", sess.Status)
			}
		})
	}
}

func TestSecondFinalizeKeepsFirstReviewer(t *testing.T) {
	env := newRBACEnv(t)
	path := fmt.Sprintf("/review/%d/finalize", env.sessGraded)
	if rec := env.do("teacher", http.MethodPost, path, url.Values{"final_grade": {"70"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("first finalize: status %d", rec.Code)
	}
	if rec := env.do("admin", http.MethodPost, path, url.Values{"final_grade": {"95"}}); rec.Code != http.StatusConflict {
		t.Fatalf("second finalize: status %d, want 409", rec.Code)
	}
	g, err := env.store.GetGrade(env.sessGraded)
	if err != nil || g == nil || g.FinalGrade == nil {
		t.Fatalf("GetGrade: %v, %+v", err, g)
	}
	if *g.FinalGrade != 70 {
		t.Errorf("final grade %v, want 70", *g.FinalGrade)
	}
	teacher, err := env.store.GetUserByUsername("teacher")
	if err != nil || teacher == nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if g.ReviewedBy == nil || *g.ReviewedBy != teacher.ID {
		t.Errorf("reviewed_by %v, want teacher %d", g.ReviewedBy, teacher.ID)
	}
}
