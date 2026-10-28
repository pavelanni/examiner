package handler

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func newImportStore(t *testing.T) *store.Store {
	t.Helper()
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// The request users of these tests: admin (ID 1) and teacher1 (ID 2).
	for _, u := range []model.User{
		{Username: "admin", DisplayName: "Administrator", Role: model.UserRoleAdmin},
		{Username: "teacher1", DisplayName: "T", Role: model.UserRoleTeacher},
	} {
		u.PasswordHash, u.Active = "x", true
		if _, err := s.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// importQuestions returns a JSON array of n valid questions; bad maps a
// 1-based question number to a replacement for its max_points/rubric pair.
func importQuestions(n int, bad map[int]string) string {
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		tail := `"rubric":"r","max_points":5`
		if b, ok := bad[i]; ok {
			tail = b
		}
		parts = append(parts, fmt.Sprintf(`{"text":"Question %d","difficulty":"easy","topic":"t","model_answer":"a",%s}`, i, tail))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func adminUpload(t *testing.T, h *Handler, filename, content string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("questions_file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/questions", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	admin := &model.User{ID: 1, Role: model.UserRoleAdmin}
	req = req.WithContext(i18n.WithLocalizer(model.ContextWithUser(req.Context(), admin), i18n.NewLocalizer("en")))
	rec := httptest.NewRecorder()
	h.handleUploadQuestions(rec, req)
	return rec
}

func questionCount(t *testing.T, s *store.Store) int {
	t.Helper()
	n, err := s.QuestionCount()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdminImportRejectsWholeFile(t *testing.T) {
	s := newImportStore(t)
	h := &Handler{store: s}
	rec := adminUpload(t, h, "q.json", importQuestions(10, map[int]string{7: `"rubric":"r","max_points":2.5`}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Question 7, field max_points") {
		t.Errorf("body does not name question 7 and its field: %s", rec.Body.String())
	}
	if n := questionCount(t, s); n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
	if h, _ := s.GetImportedFileHash("q.json"); h != "" {
		t.Errorf("file hash recorded for a rejected file")
	}
}

func TestAdminImportReportsAllErrors(t *testing.T) {
	s := newImportStore(t)
	rec := adminUpload(t, &Handler{store: s}, "q.json", importQuestions(6, map[int]string{
		2: `"rubric":"","max_points":5`,
		5: `"rubric":"r","max_points":0`,
	}))
	for _, want := range []string{"Question 2, field rubric", "Question 5, field max_points"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestAdminImportRetryAfterFix(t *testing.T) {
	s := newImportStore(t)
	h := &Handler{store: s}
	adminUpload(t, h, "q.json", importQuestions(10, map[int]string{7: `"rubric":"","max_points":5`}))
	rec := adminUpload(t, h, "q.json", importQuestions(10, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
	}
	if n := questionCount(t, s); n != 10 {
		t.Errorf("count = %d, want 10", n)
	}
}

func TestAdminImportSummaryAndWarnings(t *testing.T) {
	s := newImportStore(t)
	content := `[{"text":"What is X?","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":5},` +
		`{"text":" what is x ","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":5}]`
	rec := adminUpload(t, &Handler{store: s}, "q.json", content)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"Imported: 1, updated: 0, skipped: 1.", "Question 2 duplicates question 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestAdminImportAlreadyImportedIsTranslated(t *testing.T) {
	s := newImportStore(t)
	h := &Handler{store: s}
	adminUpload(t, h, "q.json", importQuestions(1, nil))
	rec := adminUpload(t, h, "q.json", importQuestions(1, nil))
	if !strings.Contains(rec.Body.String(), "This file has already been imported.") {
		t.Errorf("duplicate-file message not translated: %s", rec.Body.String())
	}
}
