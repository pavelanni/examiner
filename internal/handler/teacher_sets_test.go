package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func profilePage(t *testing.T, h *Handler, query string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleTeacherProfile(rec, withUser(httptest.NewRequest(http.MethodGet, "/teacher/profile"+query, nil), teacher1User))
	if rec.Code != http.StatusOK {
		t.Fatalf("profile: status %d", rec.Code)
	}
	return rec.Body.String()
}

// Saving an edited set under the name the form shows must not rename the
// file: the name used to gain a second teacher_<username>_ prefix.
func TestTeacherResaveKeepsFileName(t *testing.T) {
	h, s := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	// The edit form used to fill the name field with the file name.
	rec := teacherSave(t, h, map[string]string{
		"questions_json": importQuestions(2, nil), "custom_filename": "teacher_teacher1_mid",
		"editing_file": "teacher_teacher1_mid.json", "question_ids": idsOf(t, s, "Question 1", "Question 2"),
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	if files := questionFiles(t); !slices.Equal(files, []string{"teacher_teacher1_mid.json"}) {
		t.Errorf("questions/ = %v; want the file kept", files)
	}
}

func TestTeacherOwnPrefixNotDoubled(t *testing.T) {
	h, _ := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(1, nil), "custom_filename": "teacher_teacher1_mid.json"})
	if files := questionFiles(t); !slices.Equal(files, []string{"teacher_teacher1_mid.json"}) {
		t.Errorf("questions/ = %v; want teacher_teacher1_mid.json", files)
	}
}

func TestTeacherSaveRedirectsWithSummary(t *testing.T) {
	h, _ := teacherEnv(t)
	rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	loc, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || err != nil {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	q := loc.Query()
	if loc.Path != "/teacher/profile" || q.Get("saved") != "mid" || q.Get("inserted") != "2" || q.Get("updated") != "0" || q.Get("skipped") != "0" {
		t.Errorf("location = %s", loc)
	}
}

func TestTeacherProfileShowsSetsAndSummary(t *testing.T) {
	h, _ := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	body := profilePage(t, h, "?saved=mid&inserted=2&updated=0&skipped=0")
	for _, want := range []string{
		"Set «mid» saved: 2 added",
		">mid<",
		`/teacher/create-test?file=teacher_teacher1_mid.json`,
		`/teacher/tests/file/teacher_teacher1_mid.json`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("profile lacks %q", want)
		}
	}
	if strings.Contains(body, ">teacher_teacher1_mid<") {
		t.Error("the set is listed under its file name")
	}
}

func TestTeacherProfileEmpty(t *testing.T) {
	h, _ := teacherEnv(t)
	if body := profilePage(t, h, ""); !strings.Contains(body, "You have no question sets yet") {
		t.Error("empty state missing")
	}
}

func TestTeacherProfileIgnoresBadSummary(t *testing.T) {
	h, _ := teacherEnv(t)
	body := profilePage(t, h, "?saved=%3Cscript%3E&inserted=x&updated=0&skipped=0")
	if strings.Contains(body, " saved: ") || strings.Contains(body, "<script>x") || strings.Contains(body, "&lt;script&gt;") {
		t.Error("a summary with bad counts is shown")
	}
	body = profilePage(t, h, "?saved=%3Cb%3Ehi%3C%2Fb%3E&inserted=1&updated=0&skipped=0")
	if strings.Contains(body, "<b>hi</b>") || !strings.Contains(body, "&lt;b&gt;hi&lt;/b&gt;") {
		t.Error("the set name in the summary is not escaped")
	}
}
