package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

func compileTestQuestionSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	absSchema, err := filepath.Abs("../../schema/question_schema.json")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	f, err := os.Open(absSchema)
	if err != nil {
		t.Fatalf("os.Open(%s): %v", absSchema, err)
	}
	defer func() { _ = f.Close() }()
	schemaURL := "file://" + filepath.ToSlash(absSchema)
	if err := compiler.AddResource(schemaURL, f); err != nil {
		t.Fatalf("AddResource: %v", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatalf("Compile schema: %v", err)
	}
	return schema
}

func TestTeacherUploadDuplicateProtection(t *testing.T) {
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}

	schema := compileTestQuestionSchema(t)

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	if err := os.MkdirAll("questions", 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h := &Handler{store: s, questionSchema: schema}
	teacher := &model.User{ID: 2, Username: "alice", DisplayName: "Alice", Role: model.UserRoleTeacher}

	postTeacherJSON := func(questionsJSON, customFilename, editingFile, lang string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("questions_json", questionsJSON)
		if customFilename != "" {
			_ = writer.WriteField("custom_filename", customFilename)
		}
		if editingFile != "" {
			_ = writer.WriteField("editing_file", editingFile)
		}
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/teacher/tests", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		ctx := i18n.WithLocalizer(model.ContextWithUser(req.Context(), teacher), i18n.NewLocalizer(lang))
		req = req.WithContext(ctx)

		rec := httptest.NewRecorder()
		h.handleTeacherUpload(rec, req)
		return rec
	}

	// 1. Intra-file duplicate in teacher upload produces localized warning and saves only 1 question.
	dupJSON := `[
		{"text": "Что такое ПИД-регулятор?", "difficulty": "easy", "topic": "control", "rubric": "r1", "model_answer": "a1", "max_points": 5},
		{"text": "  что  такое  пид-регулятор  ", "difficulty": "easy", "topic": "control", "rubric": "r2", "model_answer": "a2", "max_points": 5}
	]`
	rec1 := postTeacherJSON(dupJSON, "pid_quiz", "", "en")
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with warning banner, got %d (%s)", rec1.Code, rec1.Body.String())
	}
	if !strings.Contains(rec1.Body.String(), "matches question #1 in the file under topic") {
		t.Errorf("expected English intra-file duplicate warning, got: %s", rec1.Body.String())
	}
	count, err := s.QuestionCount()
	if err != nil {
		t.Fatalf("QuestionCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 question in DB after intra-file dedup, got %d", count)
	}

	// 2. Editing/re-saving teacher's own question updates it idempotently without creating a duplicate.
	editJSON := `[
		{"text": "Что такое ПИД-регулятор", "difficulty": "medium", "topic": "control", "rubric": "updated", "model_answer": "a1", "max_points": 8}
	]`
	rec2 := postTeacherJSON(editJSON, "pid_quiz", "teacher_alice_pid_quiz.json", "en")
	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on clean edit, got %d (%s)", rec2.Code, rec2.Body.String())
	}
	count, err = s.QuestionCount()
	if err != nil {
		t.Fatalf("QuestionCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected question count to remain 1, got %d", count)
	}
	found, err := s.FindQuestionByTopicAndTextNorm("control", model.NormalizeQuestionText("Что такое ПИД-регулятор?"))
	if err != nil || found == nil {
		t.Fatalf("FindQuestionByTopicAndTextNorm: %v, %+v", err, found)
	}
	if found.Rubric != "updated" || found.MaxPoints != 8 {
		t.Errorf("expected own question to be updated, got %+v", found)
	}

	// 3. Question already in DB from another source (e.g. admin import) is NOT overwritten and warns in RU.
	adminQID, err := s.InsertQuestion(model.Question{
		CourseID:    1,
		Text:        "What is Kubernetes?",
		Difficulty:  model.DifficultyEasy,
		Topic:       "devops",
		Rubric:      "admin rubric",
		ModelAnswer: "admin answer",
		MaxPoints:   10,
	})
	if err != nil {
		t.Fatalf("InsertQuestion admin: %v", err)
	}

	conflictJSON := `[
		{"text": "what is kubernetes", "difficulty": "hard", "topic": "devops", "rubric": "hijacked", "model_answer": "hijacked", "max_points": 1}
	]`
	rec3 := postTeacherJSON(conflictJSON, "devops_quiz", "", "ru")
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with warning banner, got %d", rec3.Code)
	}
	if !strings.Contains(rec3.Body.String(), "совпадает с существующим вопросом #") {
		t.Errorf("expected Russian DB duplicate warning, got: %s", rec3.Body.String())
	}
	adminQ, err := s.GetQuestion(adminQID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if adminQ.Rubric != "admin rubric" || adminQ.MaxPoints != 10 {
		t.Errorf("unowned question was overwritten: %+v", adminQ)
	}

	// 4. Re-uploading when a legacy near-duplicate exists in the same topic does NOT return HTTP 500.
	if _, err := s.InsertQuestion(model.Question{
		CourseID: 1, Text: "Что такое ПИД-регулятор", Difficulty: model.DifficultyEasy, Topic: "control", MaxPoints: 5,
	}); err != nil {
		t.Fatalf("InsertQuestion legacy near-duplicate: %v", err)
	}
	rec4 := postTeacherJSON(editJSON, "pid_quiz", "teacher_alice_pid_quiz.json", "en")
	if rec4.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect even with legacy near-duplicate, got %d (%s)", rec4.Code, rec4.Body.String())
	}
}

func TestStartExamDeduplicatesByTextNorm(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Test"}); err != nil {
		t.Fatalf("CreateBlueprint: %v", err)
	}
	if _, err := s.InsertQuestion(model.Question{
		CourseID: 1, Text: "What is Go?", Difficulty: model.DifficultyEasy, Topic: "go", MaxPoints: 5,
	}); err != nil {
		t.Fatalf("InsertQuestion 1: %v", err)
	}
	if _, err := s.InsertQuestion(model.Question{
		CourseID: 1, Text: "what is go", Difficulty: model.DifficultyEasy, Topic: "go", MaxPoints: 5,
	}); err != nil {
		t.Fatalf("InsertQuestion 2: %v", err)
	}
	if _, err := s.InsertQuestion(model.Question{
		CourseID: 1, Text: "What is Go?", Difficulty: model.DifficultyEasy, Topic: "history", MaxPoints: 5,
	}); err != nil {
		t.Fatalf("InsertQuestion 3 (different topic): %v", err)
	}

	h := &Handler{store: s, config: model.ExamConfig{}}
	req := httptest.NewRequest(http.MethodPost, "/exam/start", nil)
	req = req.WithContext(model.ContextWithUser(req.Context(), &model.User{ID: 1, Role: model.UserRoleStudent}))

	rec := httptest.NewRecorder()
	h.handleStartExam(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("handleStartExam status = %d, body: %s", rec.Code, rec.Body.String())
	}

	sessions, err := s.ListSessionsByUser(1)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("ListSessionsByUser: err=%v, sessions=%+v", err, sessions)
	}
	threads, err := s.GetThreadsForSession(sessions[0].ID)
	if err != nil {
		t.Fatalf("GetThreadsForSession: %v", err)
	}
	// 2 in "go" collapse to 1, plus 1 in "history" = 2 threads total.
	if len(threads) != 2 {
		t.Errorf("expected 2 threads (1 per topic), got %d", len(threads))
	}
}

func TestTeacherUploadRedirectRespectsBasePath(t *testing.T) {
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Change to repository root so compileQuestionSchema() finds schema/question_schema.json.
	t.Chdir(filepath.Join("..", ".."))

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
	t.Chdir(tmpDir)
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
