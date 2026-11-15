package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// teacherEnv runs in a temp working directory with an empty questions/.
func teacherEnv(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	s := newImportStore(t)
	t.Chdir(t.TempDir())
	if err := os.Mkdir("questions", 0o755); err != nil {
		t.Fatal(err)
	}
	return &Handler{store: s}, s
}

func teacherSave(t *testing.T, h *Handler, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/teacher/tests", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	teacher := &model.User{ID: 2, Username: "teacher1", DisplayName: "T", Role: model.UserRoleTeacher}
	req = req.WithContext(i18n.WithLocalizer(model.ContextWithUser(req.Context(), teacher), i18n.NewLocalizer("en")))
	rec := httptest.NewRecorder()
	h.handleTeacherUpload(rec, req)
	return rec
}

func questionFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("questions")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestTeacherSaveRejectsWholeFile(t *testing.T) {
	h, s := teacherEnv(t)
	rec := teacherSave(t, h, map[string]string{
		"questions_json":  importQuestions(10, map[int]string{7: `"rubric":"","max_points":5`}),
		"custom_filename": "mid",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Question 7, field rubric") {
		t.Errorf("body does not name question 7 and its field")
	}
	if n := questionCount(t, s); n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
	if files := questionFiles(t); len(files) != 0 {
		t.Errorf("questions/ = %v, want empty", files)
	}
}

func TestTeacherSaveRerendersInput(t *testing.T) {
	h, _ := teacherEnv(t)
	rec := teacherSave(t, h, map[string]string{
		"questions_json":  importQuestions(3, map[int]string{2: `"rubric":"r","max_points":0`}),
		"custom_filename": "midterm-draft",
	})
	body := rec.Body.String()
	if !strings.Contains(body, "Question 3") || !strings.Contains(body, `id="preloaded-data"`) {
		t.Errorf("submitted questions not preloaded")
	}
	if !strings.Contains(body, `value="midterm-draft"`) {
		t.Errorf("custom filename not kept")
	}
}

func TestTeacherSaveSuccess(t *testing.T) {
	h, s := teacherEnv(t)
	rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/teacher/profile" {
		t.Fatalf("status = %d, location = %q", rec.Code, rec.Header().Get("Location"))
	}
	if files := questionFiles(t); !slices.Equal(files, []string{"teacher_teacher1_mid.json"}) {
		t.Errorf("questions/ = %v", files)
	}
	if n := questionCount(t, s); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

func TestTeacherEditInvalidKeepsOldState(t *testing.T) {
	h, s := teacherEnv(t)
	original := importQuestions(2, nil)
	teacherSave(t, h, map[string]string{"questions_json": original, "custom_filename": "mid"})

	rec := teacherSave(t, h, map[string]string{
		"questions_json":  importQuestions(1, map[int]string{1: `"rubric":"","max_points":5`}),
		"editing_file":    "teacher_teacher1_mid.json",
		"custom_filename": "final",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if files := questionFiles(t); !slices.Equal(files, []string{"teacher_teacher1_mid.json"}) {
		t.Errorf("questions/ = %v, want only the original file", files)
	}
	data, err := os.ReadFile(filepath.Join("questions", "teacher_teacher1_mid.json"))
	if err != nil || string(data) != original {
		t.Errorf("original file changed: %q, %v", data, err)
	}
	qs, err := s.ListQuestions()
	if err != nil || len(qs) != 2 || qs[0].Rubric != "r" {
		t.Errorf("questions changed: %+v, %v", qs, err)
	}
}

func TestTeacherEditRenameRemovesOld(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	rec := teacherSave(t, h, map[string]string{
		"questions_json":  importQuestions(1, nil),
		"editing_file":    "teacher_teacher1_mid.json",
		"custom_filename": "final",
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
	}
	if files := questionFiles(t); !slices.Equal(files, []string{"teacher_teacher1_final.json"}) {
		t.Errorf("questions/ = %v", files)
	}
	if n := questionCount(t, s); n != 1 {
		t.Errorf("count = %d, want 1 (dropped question deleted)", n)
	}
}

func TestTeacherSaveTxFailureLeavesNoFile(t *testing.T) {
	h, s := teacherEnv(t)
	_ = s.Close()
	rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sql:") {
		t.Errorf("internal error leaked to the response")
	}
	if files := questionFiles(t); len(files) != 0 {
		t.Errorf("questions/ = %v, want empty", files)
	}
}

func TestTeacherSaveRejectsBadFilename(t *testing.T) {
	for name, custom := range map[string]string{
		"too long":       strings.Repeat("ф", 150), // 300 bytes
		"reserved chars": "mid:term?",
		"control char":   "mid\tterm",
	} {
		t.Run(name, func(t *testing.T) {
			h, s := teacherEnv(t)
			rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": custom})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "file name") {
				t.Errorf("body does not explain the file name problem")
			}
			if n := questionCount(t, s); n != 0 {
				t.Errorf("count = %d, want 0", n)
			}
			if files := questionFiles(t); len(files) != 0 {
				t.Errorf("questions/ = %v, want empty", files)
			}
		})
	}
}

func TestTeacherSavePublishFailureRollsBack(t *testing.T) {
	h, s := teacherEnv(t)
	// A directory at the destination makes publishing the file fail.
	if err := os.Mkdir(filepath.Join("questions", "teacher_teacher1_mid.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Nothing was changed") {
		t.Errorf("a failed publish rolls back, so the page must say nothing changed")
	}
	if n := questionCount(t, s); n != 0 {
		t.Errorf("count = %d, want 0 (transaction rolled back)", n)
	}
	if h, err := s.GetImportedFileHash("teacher_teacher1_mid.json"); err != nil || h != "" {
		t.Errorf("imported file hash = %q, %v; want none", h, err)
	}
	if files := questionFiles(t); !slices.Equal(files, []string{"teacher_teacher1_mid.json"}) {
		t.Errorf("questions/ = %v, want only the blocking directory (no staging files)", files)
	}
}

// insertForeign stores "Question 1" in topic t as if another source (an
// admin import) had created it.
func insertForeign(t *testing.T, s *store.Store) int64 {
	t.Helper()
	id, err := s.InsertQuestion(model.Question{CourseID: 1, Text: "Question 1", Difficulty: model.DifficultyEasy,
		Topic: "t", Rubric: "foreign", ModelAnswer: "a", MaxPoints: 3})
	if err != nil || id == 0 {
		t.Fatalf("InsertQuestion = %d, %v", id, err)
	}
	return id
}

func TestTeacherSaveWritesOnlyAcceptedQuestions(t *testing.T) {
	h, s := teacherEnv(t)
	foreign := insertForeign(t, s)
	rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "matches existing question #") {
		t.Fatalf("status = %d; want 200 with the skip warning; body: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join("questions", "teacher_teacher1_mid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"Question 1"`) || !strings.Contains(string(data), `"Question 2"`) {
		t.Errorf("file = %s; want only Question 2", data)
	}
	// The skipped question is not in the teacher's file, so a second save
	// still may not change it.
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil),
		"editing_file": "teacher_teacher1_mid.json", "custom_filename": "mid"})
	if q, err := s.GetQuestion(foreign); err != nil || q.Rubric != "foreign" {
		t.Errorf("foreign question = %+v, %v; want unchanged", q, err)
	}
}

func TestTeacherSaveNothingAcceptedChangesNothing(t *testing.T) {
	h, s := teacherEnv(t)
	insertForeign(t, s)
	rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(1, nil), "custom_filename": "mid"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "skipped: 1") {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	if files := questionFiles(t); len(files) != 0 {
		t.Errorf("questions/ = %v, want no file", files)
	}
	if h, err := s.GetImportedFileHash("teacher_teacher1_mid.json"); err != nil || h != "" {
		t.Errorf("imported file hash = %q, %v; want none", h, err)
	}
}
