package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func TestTeacherUploadRedirectRespectsBasePath(t *testing.T) {
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Save original directory (internal/handler).
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(pkgDir) })

	// Change to repository root so compileQuestionSchema() finds schema/question_schema.json.
	rootDir := filepath.Clean(filepath.Join(pkgDir, "..", ".."))
	if err := os.Chdir(rootDir); err != nil {
		t.Fatalf("os.Chdir root: %v", err)
	}

	basePath := "/ru"
	cfg := model.ExamConfig{
		BasePath: basePath,
	}

	h, err := New(s, nil, cfg)
	if err != nil {
		t.Fatalf("New handler: %v", err)
	}

	// Change to a temporary directory so the uploaded question file is isolated.
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("os.Chdir tmpDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "questions"), 0755); err != nil {
		t.Fatalf("os.MkdirAll questions: %v", err)
	}

	// Prepare multipart form with question JSON data.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("questions_json", `[{"text":"Q1","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":10}]`)
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, basePath+"/teacher/tests", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Authenticate request as a teacher user (including Username for file naming).
	teacherUser := &model.User{ID: 1, Username: "teacher1", Role: model.UserRoleTeacher}
	ctx := i18n.WithLocalizer(model.ContextWithUser(req.Context(), teacherUser), i18n.NewLocalizer("en"))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.handleTeacherUpload(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", rec.Code, http.StatusSeeOther, rec.Body.String())
	}

	// Verify redirect location includes the configured BasePath prefix.
	expectedLocation := "/ru/teacher/profile"
	if loc := rec.Header().Get("Location"); loc != expectedLocation {
		t.Errorf("Location = %q, want %q", loc, expectedLocation)
	}
}
