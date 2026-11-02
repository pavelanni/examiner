package handler

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// idsOf returns the constructor's question_ids field for the questions
// with these texts, 0 for a text that is not stored.
func idsOf(t *testing.T, s *store.Store, texts ...string) string {
	t.Helper()
	ids := make([]int64, 0, len(texts))
	for _, text := range texts {
		var id int64
		if q := questionByText(t, s, text); q != nil {
			id = q.ID
		}
		ids = append(ids, id)
	}
	b, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var dupTokenRe = regexp.MustCompile(`name="dup_token" value="([0-9a-f]+)"`)

// confirmDups answers the duplicate form of rec, a 409 response to fields,
// with choices by question number, and submits the form again.
func confirmDups(t *testing.T, h *Handler, user *model.User, fields map[string]string, rec *httptest.ResponseRecorder, choices map[int]string) *httptest.ResponseRecorder {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 with duplicate choices", rec.Code)
	}
	m := dupTokenRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("no dup_token in the duplicate form")
	}
	f := maps.Clone(fields)
	f["dup_token"] = m[1]
	for n, c := range choices {
		f[fmt.Sprintf("dup_%d", n)] = c
	}
	return teacherSaveAs(t, h, user, f)
}

func readQuestionFile(t *testing.T, name string) []model.QuestionImport {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("questions", name))
	if err != nil {
		t.Fatal(err)
	}
	var qs []model.QuestionImport
	if err := json.Unmarshal(data, &qs); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return qs
}

func TestConstructorPrefillsIDs(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	body := createTestPage(t, h, "teacher_teacher1_mid.json")
	for _, text := range []string{"Question 1", "Question 2"} {
		q := questionByText(t, s, text)
		// templ escapes the quotes of the JSON in the attribute value.
		if want := fmt.Sprintf("&#34;id&#34;:%d,&#34;text&#34;:&#34;%s&#34;", q.ID, text); !strings.Contains(body, want) {
			t.Errorf("preloaded data lacks %s", want)
		}
	}
}

func TestTeacherEditRewordsByID(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": oneQ("What is OOP", "r"), "custom_filename": "mid"})
	id := questionByText(t, s, "What is OOP").ID

	rec := teacherSave(t, h, map[string]string{
		"questions_json": oneQ("What is object-oriented programming", "r2"),
		"question_ids":   fmt.Sprintf("[%d]", id),
		"editing_file":   "teacher_teacher1_mid.json",
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	q, err := s.GetQuestion(id)
	if err != nil || q.Text != "What is object-oriented programming" || q.Rubric != "r2" {
		t.Errorf("question #%d = %+v, %v; want reworded in place", id, q, err)
	}
	if n := questionCount(t, s); n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}

func TestTeacherResaveUnchangedKeepsCount(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(3, nil), "custom_filename": "mid"})
	for range 2 {
		rec := teacherSave(t, h, map[string]string{
			"questions_json": importQuestions(3, nil),
			"question_ids":   idsOf(t, s, "Question 1", "Question 2", "Question 3"),
			"editing_file":   "teacher_teacher1_mid.json",
		})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
		}
		if n := questionCount(t, s); n != 3 {
			t.Errorf("count = %d, want 3", n)
		}
	}
}

func TestTeacherIDsLimitedToEditedFile(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Mine", "r"), "custom_filename": "mid"})
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Other file", "r"), "custom_filename": "other"})
	other := questionByText(t, s, "Other file")

	// A block may only name a question of the file being edited.
	rec := teacherSave(t, h, map[string]string{
		"questions_json": oneQ("Hijack", "r"),
		"question_ids":   fmt.Sprintf("[%d]", other.ID),
		"editing_file":   "teacher_teacher1_mid.json",
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	if q, err := s.GetQuestion(other.ID); err != nil || q.Text != "Other file" {
		t.Errorf("question of another file changed: %+v, %v", q, err)
	}
	if questionByText(t, s, "Hijack") == nil || questionByText(t, s, "Mine") != nil {
		t.Error("want Hijack saved as a new question and Mine (dropped from the file) deleted")
	}
}

func TestTeacherDuplicatesInFileAsked(t *testing.T) {
	h, s := teacherEnv(t)
	fields := map[string]string{
		"questions_json":  twoQ("What is a PID controller?", "r", "  what is a pid  controller ", "r"),
		"custom_filename": "mid",
	}
	rec := teacherSave(t, h, fields)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Question 2 duplicates question 1 in this test") {
		t.Errorf("duplicate not explained")
	}
	if n := questionCount(t, s); n != 0 {
		t.Fatalf("count = %d before the decision, want 0", n)
	}
	if files := questionFiles(t); len(files) != 0 {
		t.Fatalf("questions/ = %v before the decision, want empty", files)
	}

	rec = confirmDups(t, h, teacher1User, fields, rec, map[int]string{2: "skip"})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	if n := questionCount(t, s); n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
	if qs := readQuestionFile(t, "teacher_teacher1_mid.json"); len(qs) != 1 || qs[0].Text != "What is a PID controller?" {
		t.Errorf("file = %+v; want only the saved question", qs)
	}
}

func TestTeacherOwnDuplicateSkipOrUpdate(t *testing.T) {
	for _, choice := range []string{"skip", "update"} {
		t.Run(choice, func(t *testing.T) {
			h, s := teacherEnv(t)
			teacherSave(t, h, map[string]string{"questions_json": oneQ("Shared Q", "old"), "custom_filename": "mid"})
			fields := map[string]string{"questions_json": twoQ("shared q.", "new", "Fresh", "r"), "custom_filename": "final"}
			rec := teacherSave(t, h, fields)
			body := rec.Body.String()
			if !strings.Contains(body, "Question 1 matches your question #1") || !strings.Contains(body, `value="update"`) {
				t.Errorf("own duplicate not offered for update")
			}
			rec = confirmDups(t, h, teacher1User, fields, rec, map[int]string{1: choice})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
			}
			q, err := s.GetQuestion(1)
			if err != nil {
				t.Fatal(err)
			}
			wantText, wantFile := "Shared Q", 1
			if choice == "update" {
				wantText, wantFile = "shared q.", 2
			}
			if q.Text != wantText || questionCount(t, s) != 2 {
				t.Errorf("#1 = %+v, count %d; want text %q and 2 questions", q, questionCount(t, s), wantText)
			}
			if qs := readQuestionFile(t, "teacher_teacher1_final.json"); len(qs) != wantFile {
				t.Errorf("file = %+v; want %d questions", qs, wantFile)
			}
		})
	}
}

func TestTeacherStaleDecisionsAskAgain(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Shared Q", "old"), "custom_filename": "mid"})
	fields := map[string]string{"questions_json": twoQ("shared q", "new", "Fresh", "r"), "custom_filename": "final"}
	rec := teacherSave(t, h, fields)

	// The teacher edits the test after being asked: the answers no longer apply.
	fields["questions_json"] = twoQ("shared q", "newer", "Fresh", "r")
	rec = confirmDups(t, h, teacher1User, fields, rec, map[int]string{1: "update"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409 for an edited test", rec.Code)
	}
	if q, _ := s.GetQuestion(1); q.Rubric != "old" {
		t.Errorf("#1 updated with stale decisions: %+v", q)
	}
}

func TestTeacherAllSkippedSavesNothing(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": oneQ("Shared Q", "old"), "custom_filename": "mid"})
	fields := map[string]string{"questions_json": oneQ("Shared Q", "new"), "custom_filename": "final"}
	rec := confirmDups(t, h, teacher1User, fields, teacherSave(t, h, fields), map[int]string{1: "skip"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "nothing to save") {
		t.Errorf("status %d; want 400 saying there is nothing to save", rec.Code)
	}
	if files := questionFiles(t); len(files) != 1 {
		t.Errorf("questions/ = %v; want only the first file", files)
	}
	if n := questionCount(t, s); n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}

func TestAdminImportUpdateOption(t *testing.T) {
	s := newImportStore(t)
	insertQ(t, s, ownedQ("Question 1", 2))
	rec := adminUploadWith(t, &Handler{store: s}, "q.json", importQuestions(2, nil), map[string]string{"on_duplicate": "update"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Imported: 1, updated: 1, skipped: 0") {
		t.Errorf("status %d; summary missing", rec.Code)
	}
	if q := questionByText(t, s, "Question 1"); q.OwnerID != 2 || q.Rubric != "r" {
		t.Errorf("question = %+v; want updated, owner kept", q)
	}
}

func TestAdminPageReportsStoredDuplicates(t *testing.T) {
	s := newImportStore(t)
	// Legacy rows predating the checks; the exam start dedups them by text_norm.
	for _, text := range []string{"What is X?", "what is x", "Unrelated"} {
		if _, err := s.InsertQuestion(model.Question{CourseID: 1, Text: text, Difficulty: "easy", Topic: "t", Rubric: "r", ModelAnswer: "a", MaxPoints: 1}); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{store: s}
	rec := httptest.NewRecorder()
	h.handleAdminQuestionsPage(rec, withUser(httptest.NewRequest(http.MethodGet, "/admin/questions", nil), adminUser))
	body := rec.Body.String()
	if !strings.Contains(body, "Duplicate questions in the database") || !strings.Contains(body, "questions #1, #2") {
		t.Errorf("duplicate report missing")
	}
	if strings.Contains(body, "Unrelated") {
		t.Errorf("unrelated question reported")
	}
	if n := questionCount(t, s); n != 3 {
		t.Errorf("count = %d; the report must not delete anything", n)
	}
}
