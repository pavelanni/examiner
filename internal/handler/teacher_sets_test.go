package handler

import (
	"html"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/handler/views"
	"github.com/pavelanni/examiner/internal/model"
)

func profileResponse(t *testing.T, h *Handler, summary *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/teacher/profile", nil)
	if summary != nil {
		req.AddCookie(summary)
	}
	rec := httptest.NewRecorder()
	h.handleTeacherProfile(rec, withUser(req, teacher1User))
	if rec.Code != http.StatusOK {
		t.Fatalf("profile: status %d", rec.Code)
	}
	return rec
}

// profilePage renders the profile, with the save summary cookie if given.
func profilePage(t *testing.T, h *Handler, summary *http.Cookie) string {
	t.Helper()
	return profileResponse(t, h, summary).Body.String()
}

// summaryCookie is the save summary cookie the save response sets.
func summaryCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == saveSummaryCookie {
			return c
		}
	}
	t.Fatal("no save summary cookie")
	return nil
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

// The summary of a save travels in a short-lived cookie, not in the URL, so
// a reload or a shared link does not show it again.
func TestTeacherSaveRedirectsWithSummary(t *testing.T) {
	h, _ := teacherEnv(t)
	rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/teacher/profile" {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	c := summaryCookie(t, rec)
	if c.Path != "/teacher/profile" || !c.HttpOnly || c.MaxAge <= 0 {
		t.Errorf("cookie = %+v", c)
	}
	q, err := url.ParseQuery(c.Value)
	if err != nil || q.Get("saved") != "mid" || q.Get("inserted") != "2" || q.Get("updated") != "0" || q.Get("skipped") != "0" {
		t.Errorf("cookie value = %q, %v", c.Value, err)
	}
}

// The profile shows the summary once and clears the cookie.
func TestTeacherProfileShowsSummaryOnce(t *testing.T) {
	h, _ := teacherEnv(t)
	c := summaryCookie(t, teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"}))
	rec := profileResponse(t, h, c)
	if !strings.Contains(rec.Body.String(), "Set «mid» saved: 2 added") {
		t.Error("summary not shown")
	}
	cleared := summaryCookie(t, rec)
	if cleared.MaxAge >= 0 || cleared.Path != "/teacher/profile" {
		t.Errorf("cookie not cleared: %+v", cleared)
	}
	if strings.Contains(profilePage(t, h, nil), " saved: ") {
		t.Error("summary shown without the cookie")
	}
}

// A summary in the URL, as older versions sent it, is not shown: anyone
// could craft such a link.
func TestTeacherProfileIgnoresSummaryInURL(t *testing.T) {
	h, _ := teacherEnv(t)
	rec := httptest.NewRecorder()
	h.handleTeacherProfile(rec, withUser(httptest.NewRequest(http.MethodGet, "/teacher/profile?saved=mid&inserted=2&updated=0&skipped=0", nil), teacher1User))
	if strings.Contains(rec.Body.String(), " saved: ") {
		t.Error("summary from the URL shown")
	}
}

func TestTeacherProfileShowsSetsAndSummary(t *testing.T) {
	h, _ := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
	body := profilePage(t, h, &http.Cookie{Name: saveSummaryCookie, Value: "saved=mid&inserted=2&updated=0&skipped=0"})
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
	if body := profilePage(t, h, nil); !strings.Contains(body, "You have no question sets yet") {
		t.Error("empty state missing")
	}
}

func TestTeacherProfileIgnoresBadSummary(t *testing.T) {
	h, _ := teacherEnv(t)
	body := profilePage(t, h, &http.Cookie{Name: saveSummaryCookie, Value: "saved=%3Cscript%3E&inserted=x&updated=0&skipped=0"})
	if strings.Contains(body, " saved: ") || strings.Contains(body, "<script>x") || strings.Contains(body, "&lt;script&gt;") {
		t.Error("a summary with bad counts is shown")
	}
	body = profilePage(t, h, &http.Cookie{Name: saveSummaryCookie, Value: "saved=%3Cb%3Ehi%3C%2Fb%3E&inserted=1&updated=0&skipped=0"})
	if strings.Contains(body, "<b>hi</b>") || !strings.Contains(body, "&lt;b&gt;hi&lt;/b&gt;") {
		t.Error("the set name in the summary is not escaped")
	}
}

func TestCommonTopic(t *testing.T) {
	q := func(topics ...string) []views.BuilderQuestion {
		var out []views.BuilderQuestion
		for _, tp := range topics {
			out = append(out, views.BuilderQuestion{QuestionImport: model.QuestionImport{Topic: tp}})
		}
		return out
	}
	for _, c := range []struct {
		in   []views.BuilderQuestion
		want string
	}{{q("a", "a"), "a"}, {q("a", "b"), ""}, {q("", "a"), "a"}, {nil, ""}} {
		if got := commonTopic(c.in); got != c.want {
			t.Errorf("commonTopic(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The profile links to every set; a name with a comma or a space reaches
// the handler percent-encoded (chi reads the raw path) and must still be
// found.
func TestTeacherDownloadEncodedName(t *testing.T) {
	h, _ := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(1, nil), "custom_filename": "Механика, 1 курс"})
	r := chi.NewRouter()
	r.Get("/teacher/tests/file/{name}", h.handleTeacherDownload)
	body := profilePage(t, h, nil)
	m := regexp.MustCompile(`href="(/teacher/tests/file/[^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no download link on the profile")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withUser(httptest.NewRequest(http.MethodGet, m[1], nil), teacher1User))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Question 1") {
		t.Errorf("GET %s: %d %q", m[1], rec.Code, rec.Body.String())
	}
	// An encoded path separator is still refused.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, withUser(httptest.NewRequest(http.MethodGet, "/teacher/tests/file/..%2Fteacher_teacher1_x.json", nil), teacher1User))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("encoded ../: status %d, want 400", rec.Code)
	}
}

// A file the old double-prefix bug created, or one ending in .json.json,
// keeps its name when the set is saved with the name field unchanged.
func TestTeacherResaveKeepsLegacyFileName(t *testing.T) {
	for _, file := range []string{"teacher_teacher1_teacher_teacher1_mid.json", "teacher_teacher1_x.json.json"} {
		t.Run(file, func(t *testing.T) {
			h, s := teacherEnv(t)
			if err := os.WriteFile(filepath.Join("questions", file), []byte(importQuestions(1, nil)), 0o644); err != nil {
				t.Fatal(err)
			}
			body := createTestPage(t, h, file)
			m := regexp.MustCompile(`name="custom_filename" value="([^"]*)"`).FindStringSubmatch(body)
			if m == nil {
				t.Fatal("no name field")
			}
			rec := teacherSave(t, h, map[string]string{
				"questions_json": importQuestions(1, nil), "custom_filename": html.UnescapeString(m[1]),
				"editing_file": file, "question_ids": idsOf(t, s, "Question 1"),
			})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
			}
			if files := questionFiles(t); !slices.Equal(files, []string{file}) {
				t.Errorf("questions/ = %v; want %s kept", files, file)
			}
		})
	}
}

// A literal % in a set name reaches chi already decoded (no RawPath).
func TestTeacherDownloadPercentName(t *testing.T) {
	h, _ := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(1, nil), "custom_filename": "100%"})
	r := chi.NewRouter()
	r.Get("/teacher/tests/file/{name}", h.handleTeacherDownload)
	m := regexp.MustCompile(`href="(/teacher/tests/file/[^"]+)"`).FindStringSubmatch(profilePage(t, h, nil))
	if m == nil {
		t.Fatal("no download link")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withUser(httptest.NewRequest(http.MethodGet, m[1], nil), teacher1User))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Question 1") {
		t.Errorf("GET %s: %d %q", m[1], rec.Code, rec.Body.String())
	}
}

// A new set, or an edited set renamed, must not take the name of another
// set: the other file would be replaced and its questions lost from view.
func TestTeacherSaveRejectsTakenSetName(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"new set":     {},
		"renamed set": {"editing_file": "teacher_teacher1_other.json"},
	} {
		t.Run(name, func(t *testing.T) {
			h, s := teacherEnv(t)
			teacherSave(t, h, map[string]string{"questions_json": importQuestions(2, nil), "custom_filename": "mid"})
			midPath := filepath.Join("questions", "teacher_teacher1_mid.json")
			before, err := os.ReadFile(midPath)
			if err != nil {
				t.Fatal(err)
			}
			fields := map[string]string{"questions_json": oneQ("Another", "r"), "custom_filename": "mid"}
			if extra["editing_file"] != "" {
				teacherSave(t, h, map[string]string{"questions_json": oneQ("Another", "r"), "custom_filename": "other"})
				fields["question_ids"] = idsOf(t, s, "Another")
			}
			for k, v := range extra {
				fields[k] = v
			}
			rec := teacherSave(t, h, fields)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "already exists") {
				t.Errorf("body does not say the set name is taken")
			}
			if after, err := os.ReadFile(midPath); err != nil || string(after) != string(before) {
				t.Errorf("existing set changed: %q, %v", after, err)
			}
		})
	}
}

// After an error the name field shows the name as the teacher sent it, so
// saving again keeps the file of a set with an old-style name.
func TestTeacherErrorKeepsLegacySetName(t *testing.T) {
	h, s := teacherEnv(t)
	const file = "teacher_teacher1_teacher_teacher1_mid.json"
	if err := os.WriteFile(filepath.Join("questions", file), []byte(importQuestions(1, nil)), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := teacherSave(t, h, map[string]string{
		"questions_json":  importQuestions(1, map[int]string{1: `"rubric":"","max_points":5`}),
		"editing_file":    file,
		"custom_filename": "teacher_teacher1_mid",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="custom_filename" value="teacher_teacher1_mid"`) {
		t.Fatalf("name field changed on re-render")
	}
	rec = teacherSave(t, h, map[string]string{
		"questions_json": importQuestions(1, nil), "editing_file": file,
		"custom_filename": "teacher_teacher1_mid", "question_ids": idsOf(t, s, "Question 1"),
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	if files := questionFiles(t); !slices.Equal(files, []string{file}) {
		t.Errorf("questions/ = %v; want %s kept", files, file)
	}
}

// A slash in a set name is refused, not silently cut to the part after it.
func TestTeacherSaveRejectsSlashInSetName(t *testing.T) {
	for _, custom := range []string{"Mechanics 1/2", `Mechanics 1\2`} {
		t.Run(custom, func(t *testing.T) {
			h, s := teacherEnv(t)
			rec := teacherSave(t, h, map[string]string{"questions_json": importQuestions(1, nil), "custom_filename": custom})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "file name") {
				t.Errorf("body does not explain the name problem")
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

// The download link saves the set as a file instead of showing the JSON.
func TestTeacherDownloadIsAttachment(t *testing.T) {
	h, _ := teacherEnv(t)
	teacherSave(t, h, map[string]string{"questions_json": importQuestions(1, nil), "custom_filename": "Механика"})
	r := chi.NewRouter()
	r.Get("/teacher/tests/file/{name}", h.handleTeacherDownload)
	rec := httptest.NewRecorder()
	target := "/teacher/tests/file/" + url.PathEscape("teacher_teacher1_Механика.json")
	r.ServeHTTP(rec, withUser(httptest.NewRequest(http.MethodGet, target, nil), teacher1User))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	_, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if cd := rec.Header().Get("Content-Disposition"); err != nil || !strings.HasPrefix(cd, "attachment") || params["filename"] != "Механика.json" {
		t.Errorf("Content-Disposition = %q (%v), params %v", cd, err, params)
	}
}
