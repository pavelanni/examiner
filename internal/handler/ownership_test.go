package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func withUser(r *http.Request, u *model.User) *http.Request {
	return r.WithContext(i18n.WithLocalizer(model.ContextWithUser(r.Context(), u), i18n.NewLocalizer("en")))
}

func insertQ(t *testing.T, s *store.Store, q model.Question) int64 {
	t.Helper()
	var id int64
	err := s.InQuestionTx(context.Background(), func(tx *store.QuestionTx) error {
		var err error
		id, err = tx.Insert(q)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func plainQ(text, topic string, published bool) model.Question {
	return model.Question{CourseID: 1, Text: text, Difficulty: model.DifficultyEasy, Topic: topic,
		Rubric: "r", ModelAnswer: "a", MaxPoints: 5, Published: published}
}

func TestStartExamUsesPublishedOnly(t *testing.T) {
	s := newImportStore(t)
	if _, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Exam", MaxFollowups: 3}); err != nil {
		t.Fatal(err)
	}
	pID := insertQ(t, s, plainQ("Published question", "t", true))
	insertQ(t, s, plainQ("Hidden question", "t", false))

	h := &Handler{store: s}
	req := httptest.NewRequest(http.MethodPost, "/exam/start", strings.NewReader(url.Values{"topic": {"t"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withUser(req, &model.User{ID: 7, Role: model.UserRoleStudent})
	rec := httptest.NewRecorder()
	h.handleStartExam(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body.String())
	}
	var sessionID int64
	if _, err := fmt.Sscanf(rec.Header().Get("Location"), "/exam/%d", &sessionID); err != nil {
		t.Fatalf("location %q: %v", rec.Header().Get("Location"), err)
	}
	threads, err := s.GetThreadsForSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].QuestionID != pID {
		t.Errorf("threads = %+v; want only the published question %d", threads, pID)
	}
}

func TestReviewShowsUnpublishedQuestion(t *testing.T) {
	s := newImportStore(t)
	bpID, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Exam", MaxFollowups: 3})
	if err != nil {
		t.Fatal(err)
	}
	q := plainQ("Question later hidden", "t", true)
	q.ID = insertQ(t, s, q)
	sessionID, err := s.CreateSession(bpID, 7, []int64{q.ID})
	if err != nil {
		t.Fatal(err)
	}
	q.Published = false
	if err := s.UpdateQuestion(q); err != nil {
		t.Fatal(err)
	}

	h := &Handler{store: s}
	r := chi.NewRouter()
	r.Get("/review/{sessionID}", h.handleReviewPage)
	req := withUser(httptest.NewRequest(http.MethodGet, fmt.Sprintf("/review/%d", sessionID), nil),
		&model.User{ID: 2, Role: model.UserRoleTeacher, DisplayName: "T"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Question later hidden") {
		t.Errorf("status = %d; question text shown = %v", rec.Code, strings.Contains(rec.Body.String(), "Question later hidden"))
	}
}

var (
	adminUser    = &model.User{ID: 1, Username: "admin", DisplayName: "Administrator", Role: model.UserRoleAdmin}
	teacher1User = &model.User{ID: 2, Username: "teacher1", DisplayName: "T", Role: model.UserRoleTeacher}
)

// addTeacherB creates a second teacher (ID 3) in the store.
func addTeacherB(t *testing.T, s *store.Store) *model.User {
	t.Helper()
	u := model.User{Username: "teacherB", DisplayName: "Petrov", PasswordHash: "x", Role: model.UserRoleTeacher, Active: true}
	id, err := s.CreateUser(u)
	if err != nil {
		t.Fatal(err)
	}
	u.ID = id
	return &u
}

func oneQ(text, rubric string) string {
	return `[{"text":"` + text + `","difficulty":"easy","topic":"t","rubric":"` + rubric + `","model_answer":"a","max_points":5}]`
}

func twoQ(a, ra, b, rb string) string {
	return `[{"text":"` + a + `","difficulty":"easy","topic":"t","rubric":"` + ra + `","model_answer":"a","max_points":5},` +
		`{"text":"` + b + `","difficulty":"easy","topic":"t","rubric":"` + rb + `","model_answer":"a","max_points":5}]`
}

func ownedQ(text string, owner int64) model.Question {
	return model.Question{CourseID: 1, Text: text, Difficulty: "easy", Topic: "t", Rubric: "r", ModelAnswer: "a",
		MaxPoints: 5, OwnerID: owner, Published: true}
}

func questionByText(t *testing.T, s *store.Store, text string) *model.Question {
	t.Helper()
	qs, err := s.ListQuestions()
	if err != nil {
		t.Fatal(err)
	}
	for i := range qs {
		if qs[i].Text == text {
			return &qs[i]
		}
	}
	return nil
}

func TestTeacherCannotOverwriteOthersQuestion(t *testing.T) {
	h, s := teacherEnv(t)
	b := addTeacherB(t, s)
	rec := teacherSave(t, h, map[string]string{"questions_json": oneQ("Shared Q", "r"), "custom_filename": "mid", "publish": "1"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("A save: %d", rec.Code)
	}
	rec = teacherSaveAs(t, h, b, map[string]string{"questions_json": oneQ("Shared Q", "hacked"), "custom_filename": "x", "publish": "1"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("B save: status %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "matches question #") || !strings.Contains(body, "owned by T") {
		t.Errorf("conflict not explained")
	}
	if q := questionByText(t, s, "Shared Q"); q == nil || q.Rubric != "r" || q.OwnerID != 2 {
		t.Errorf("A's question = %+v; want rubric r, owner 2", q)
	}
	if files := questionFiles(t); len(files) != 1 || files[0] != "teacher_teacher1_mid.json" {
		t.Errorf("questions/ = %v", files)
	}
}

func TestTeacherConflictRollsBackOwnQuestions(t *testing.T) {
	h, s := teacherEnv(t)
	b := addTeacherB(t, s)
	own := ownedQ("B own", b.ID)
	own.Rubric = "old"
	insertQ(t, s, own)
	insertQ(t, s, ownedQ("A own", 2))
	rec := teacherSaveAs(t, h, b, map[string]string{"questions_json": twoQ("B own", "new", "A own", "x"), "custom_filename": "x", "publish": "1"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if q := questionByText(t, s, "B own"); q.Rubric != "old" {
		t.Errorf("B's own question updated despite the conflict: %+v", q)
	}
}

func TestTeacherEditDoesNotDeleteOthersQuestion(t *testing.T) {
	h, s := teacherEnv(t)
	b := addTeacherB(t, s)
	insertQ(t, s, ownedQ("Shared", 2))
	insertQ(t, s, ownedQ("B question", b.ID))
	if err := os.WriteFile(filepath.Join("questions", "teacher_teacherB_mid.json"), []byte(twoQ("Shared", "r", "B question", "r")), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := teacherSaveAs(t, h, b, map[string]string{"questions_json": oneQ("B question", "r"), "editing_file": "teacher_teacherB_mid.json", "publish": "1"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	if questionByText(t, s, "Shared") == nil {
		t.Error("A's question was deleted by B's edit")
	}
}

func TestAdminConstructorUpdatesTeacherQuestion(t *testing.T) {
	h, s := teacherEnv(t)
	insertQ(t, s, ownedQ("Teacher Q", 2))
	rec := teacherSaveAs(t, h, adminUser, map[string]string{"questions_json": twoQ("Teacher Q", "admin fix", "Admin Q", "r"), "custom_filename": "adm", "publish": "1"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	if q := questionByText(t, s, "Teacher Q"); q.Rubric != "admin fix" || q.OwnerID != 2 {
		t.Errorf("teacher's question = %+v; want updated, owner kept", q)
	}
	rec = teacherSaveAs(t, h, adminUser, map[string]string{"questions_json": oneQ("Admin Q", "r"), "editing_file": "teacher_admin_adm.json", "publish": "1"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit status %d", rec.Code)
	}
	if questionByText(t, s, "Teacher Q") == nil {
		t.Error("admin's file edit deleted the teacher's question")
	}
}

func TestTeacherSaveUnpublished(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Hidden", "r"), "custom_filename": "mid"})
	if q := questionByText(t, s, "Hidden"); q == nil || q.Published {
		t.Errorf("question = %+v; want unpublished", q)
	}
	if topics, _ := s.ListPublishedTopics(); len(topics) != 0 {
		t.Errorf("topics = %v; want none", topics)
	}
}

func TestTeacherSavePublished(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Visible", "r"), "custom_filename": "mid", "publish": "1"})
	if topics, _ := s.ListPublishedTopics(); len(topics) != 1 {
		t.Errorf("topics = %v; want [t]", topics)
	}
}

var publishChecked = regexp.MustCompile(`<input[^>]*name="publish"[^>]*checked`)

func createTestPage(t *testing.T, h *Handler, file string) string {
	t.Helper()
	target := "/teacher/create-test"
	if file != "" {
		target += "?file=" + url.QueryEscape(file)
	}
	rec := httptest.NewRecorder()
	h.handleTeacherCreateTest(rec, withUser(httptest.NewRequest(http.MethodGet, target, nil), teacher1User))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", target, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="publish"`) {
		t.Fatalf("no publish checkbox on %s", target)
	}
	return rec.Body.String()
}

func TestConstructorCheckboxReflectsDB(t *testing.T) {
	h, _ := teacherEnv(t)
	if !publishChecked.MatchString(createTestPage(t, h, "")) {
		t.Error("new file: checkbox not checked")
	}
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Hidden", "r"), "custom_filename": "mid"})
	if publishChecked.MatchString(createTestPage(t, h, "teacher_teacher1_mid.json")) {
		t.Error("unpublished file: checkbox checked")
	}
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Shown", "r"), "custom_filename": "pub", "publish": "1"})
	if !publishChecked.MatchString(createTestPage(t, h, "teacher_teacher1_pub.json")) {
		t.Error("published file: checkbox not checked")
	}
}

func TestTeacherResaveKeepsUnpublished(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Hidden", "r"), "custom_filename": "mid"})
	if publishChecked.MatchString(createTestPage(t, h, "teacher_teacher1_mid.json")) {
		t.Fatal("checkbox checked for an unpublished file")
	}
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Hidden", "r2"), "editing_file": "teacher_teacher1_mid.json"})
	if q := questionByText(t, s, "Hidden"); q == nil || q.Published || q.Rubric != "r2" {
		t.Errorf("question = %+v; want updated and still unpublished", q)
	}
}

func TestAdminImportConflictImpossible(t *testing.T) {
	s := newImportStore(t)
	insertQ(t, s, ownedQ("Question 1", 2))
	rec := adminUpload(t, &Handler{store: s}, "q.json", importQuestions(1, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "matches existing question") {
		t.Errorf("status %d; want 200 with a duplicate warning", rec.Code)
	}
}

func TestAdminSaveKeepsTeacherPublication(t *testing.T) {
	h, s := teacherEnv(t)
	draft := ownedQ("Teacher draft", 2)
	draft.Published = false
	insertQ(t, s, draft)
	insertQ(t, s, ownedQ("Teacher live", 2))
	// The admin saves with the box checked, then with it unchecked.
	teacherSaveAs(t, h, adminUser, map[string]string{"questions_json": oneQ("Teacher draft", "fixed"), "custom_filename": "a1", "publish": "1"})
	teacherSaveAs(t, h, adminUser, map[string]string{"questions_json": oneQ("Teacher live", "fixed"), "custom_filename": "a2"})
	if q := questionByText(t, s, "Teacher draft"); q.Published || q.Rubric != "fixed" {
		t.Errorf("draft = %+v; want rubric updated, still unpublished", q)
	}
	if q := questionByText(t, s, "Teacher live"); !q.Published || q.Rubric != "fixed" {
		t.Errorf("live = %+v; want rubric updated, still published", q)
	}
}

// prefixEnv has users ivan and ivan_mid and a file of ivan_mid's on disk.
func prefixEnv(t *testing.T) (*Handler, *store.Store, *model.User) {
	t.Helper()
	h, s := teacherEnv(t)
	for _, name := range []string{"ivan", "ivan_mid"} {
		if _, err := s.CreateUser(model.User{Username: name, DisplayName: name, PasswordHash: "x", Role: model.UserRoleTeacher, Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	ivan, err := s.GetUserByUsername("ivan")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("questions", "teacher_ivan_mid_exam.json"), []byte(oneQ("Mid Q", "r")), 0o644); err != nil {
		t.Fatal(err)
	}
	return h, s, ivan
}

func TestPrefixSiblingCannotTouchFile(t *testing.T) {
	h, _, ivan := prefixEnv(t)
	const file = "teacher_ivan_mid_exam.json"

	rec := teacherSaveAs(t, h, ivan, map[string]string{"questions_json": oneQ("Ivan Q", "r"), "editing_file": file, "custom_filename": "other"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("save over sibling's file: status %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join("questions", file)); err != nil {
		t.Errorf("sibling's file removed: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/teacher/tests/file/{name}", h.handleTeacherDownload)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, withUser(httptest.NewRequest(http.MethodGet, "/teacher/tests/file/"+file, nil), ivan))
	if rec.Code != http.StatusForbidden {
		t.Errorf("download sibling's file: status %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.handleTeacherCreateTest(rec, withUser(httptest.NewRequest(http.MethodGet, "/teacher/create-test?file="+file, nil), ivan))
	if rec.Code != http.StatusForbidden {
		t.Errorf("open sibling's file: status %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.handleTeacherProfile(rec, withUser(httptest.NewRequest(http.MethodGet, "/teacher/profile", nil), ivan))
	if strings.Contains(rec.Body.String(), "ivan_mid_exam") {
		t.Error("ivan's profile lists ivan_mid's file")
	}
}

func TestTeacherCannotSaveIntoOthersPrefix(t *testing.T) {
	h, s, ivan := prefixEnv(t)
	rec := teacherSaveAs(t, h, ivan, map[string]string{"questions_json": oneQ("Ivan Q", "r"), "custom_filename": "mid_notes"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "another user") {
		t.Errorf("status %d; want 400 explaining the name belongs to another user", rec.Code)
	}
	if _, err := os.Stat(filepath.Join("questions", "teacher_ivan_mid_notes.json")); err == nil {
		t.Error("file created in another user's namespace")
	}
	if questionByText(t, s, "Ivan Q") != nil {
		t.Error("questions saved although the name was rejected")
	}
}
