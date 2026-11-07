package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// examStore has the exam blueprint and, in topic "t", easy/medium/hard
// published questions in the given numbers.
func examStore(t *testing.T, s *store.Store, easy, medium, hard int) {
	t.Helper()
	if _, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Exam", MaxFollowups: 3}); err != nil {
		t.Fatal(err)
	}
	for level, n := range map[model.Difficulty]int{"easy": easy, "medium": medium, "hard": hard} {
		for i := range n {
			q := plainQ(fmt.Sprintf("%s question %d", level, i), "t", true)
			q.Difficulty = level
			insertQ(t, s, q)
		}
	}
}

func student(id int64) *model.User {
	return &model.User{ID: id, Username: fmt.Sprint("s", id), DisplayName: "S", Role: model.UserRoleStudent}
}

func startExam(t *testing.T, h *Handler, user *model.User, topic string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/exam/start", strings.NewReader(url.Values{"topic": {topic}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.handleStartExam(rec, withUser(req, user))
	return rec
}

// startedSession starts an exam and returns the session it redirects to.
func startedSession(t *testing.T, h *Handler, user *model.User, topic string) int64 {
	t.Helper()
	rec := startExam(t, h, user, topic)
	var id int64
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d; body %s", rec.Code, rec.Body.String())
	}
	if _, err := fmt.Sscanf(rec.Header().Get("Location"), "/exam/%d", &id); err != nil {
		t.Fatalf("location %q: %v", rec.Header().Get("Location"), err)
	}
	return id
}

func sessionQuestions(t *testing.T, s *store.Store, sessionID int64) []model.Question {
	t.Helper()
	view, err := s.GetSessionView(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var qs []model.Question
	for _, tv := range view.Threads {
		qs = append(qs, tv.Question)
	}
	return qs
}

func questionIDs(qs []model.Question) []int64 {
	var ids []int64
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	return ids
}

func TestStartExamExactDistribution(t *testing.T) {
	s := newImportStore(t)
	examStore(t, s, 6, 6, 4)
	d := model.Distribution{"easy": 2, "medium": 2, "hard": 1}
	h := &Handler{store: s, config: model.ExamConfig{Selection: d, Shuffle: true}}

	sets := map[string]bool{}
	for i := range 50 {
		qs := sessionQuestions(t, s, startedSession(t, h, student(int64(100+i)), "t"))
		got := map[model.Difficulty]int{}
		seen := map[int64]bool{}
		for _, q := range qs {
			got[q.Difficulty]++
			if seen[q.ID] {
				t.Errorf("session %d repeats question %d", i, q.ID)
			}
			seen[q.ID] = true
		}
		if fmt.Sprint(got) != fmt.Sprint(map[model.Difficulty]int(d)) {
			t.Errorf("session %d: distribution %v, want %v", i, got, d)
		}
		ids := questionIDs(qs)
		slices.Sort(ids)
		sets[fmt.Sprint(ids)] = true
	}
	if len(sets) < 10 {
		t.Errorf("50 sessions used only %d different question sets", len(sets))
	}
}

func TestStartExamShortage(t *testing.T) {
	s := newImportStore(t)
	examStore(t, s, 3, 3, 0)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 2, "hard": 1}}}
	rec := startExam(t, h, student(7), "t")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Not enough hard questions in topic &#34;t&#34;: 1 needed, 0 available.") {
		t.Errorf("shortage not explained")
	}
	if sessions, _ := s.ListSessionsByUser(7); len(sessions) != 0 {
		t.Errorf("sessions = %v; an incomplete session must not be created", sessions)
	}
}

func TestStartExamPinsVariant(t *testing.T) {
	s := newImportStore(t)
	examStore(t, s, 5, 5, 5)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 1, "medium": 1, "hard": 1}, Shuffle: true}}
	first := startedSession(t, h, student(7), "t")
	want := questionIDs(sessionQuestions(t, s, first))

	// Starting again (another tab, a new login, a lost connection) returns
	// the same session with the same questions in the same order.
	for range 5 {
		if again := startedSession(t, h, student(7), "t"); again != first {
			t.Fatalf("second start went to session %d, want %d", again, first)
		}
		if got := questionIDs(sessionQuestions(t, s, first)); !slices.Equal(got, want) {
			t.Fatalf("questions changed: %v, want %v", got, want)
		}
	}
	if sessions, _ := s.ListSessionsByUser(7); len(sessions) != 1 {
		t.Errorf("sessions = %d, want 1", len(sessions))
	}
}

func TestStartExamNoRetakeForStudents(t *testing.T) {
	s := newImportStore(t)
	examStore(t, s, 3, 0, 0)
	h := &Handler{store: s, config: model.ExamConfig{NumQuestions: 1}}
	first := startedSession(t, h, student(7), "t")
	if err := s.UpdateSessionStatus(first, model.StatusGraded); err != nil {
		t.Fatal(err)
	}

	rec := startExam(t, h, student(7), "t")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/results/%d", first) {
		t.Errorf("status %d, location %q; want the results of session %d", rec.Code, rec.Header().Get("Location"), first)
	}
	if sessions, _ := s.ListSessionsByUser(7); len(sessions) != 1 {
		t.Errorf("sessions = %d, want 1: a student gets one attempt", len(sessions))
	}

	// A teacher may try the exam again.
	teacher := &model.User{ID: 2, Role: model.UserRoleTeacher}
	tFirst := startedSession(t, h, teacher, "t")
	if err := s.UpdateSessionStatus(tFirst, model.StatusGraded); err != nil {
		t.Fatal(err)
	}
	if again := startedSession(t, h, teacher, "t"); again == tFirst {
		t.Error("teacher's second attempt reused the finished session")
	}
}

func TestStartExamConcurrent(t *testing.T) {
	if err := i18n.Init("en"); err != nil {
		t.Fatal(err)
	}
	// A file database: every pooled connection of :memory: is a separate DB.
	s, err := store.New(filepath.Join(t.TempDir(), "exam.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	examStore(t, s, 4, 4, 4)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 1, "hard": 1}}}

	const n = 8
	locations := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			rec := startExam(t, h, student(7), "t")
			locations[i] = rec.Header().Get("Location")
		})
	}
	wg.Wait()
	for i := range n {
		if locations[i] == "" || locations[i] != locations[0] {
			t.Errorf("start %d went to %q, start 0 to %q", i, locations[i], locations[0])
		}
	}
	if sessions, _ := s.ListSessionsByUser(7); len(sessions) != 1 {
		t.Errorf("sessions = %d, want 1", len(sessions))
	}
}

func TestStartExamOrder(t *testing.T) {
	s := newImportStore(t)
	examStore(t, s, 3, 3, 3)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 2, "medium": 1, "hard": 2}}}
	qs := sessionQuestions(t, s, startedSession(t, h, student(7), "t"))
	var levels []model.Difficulty
	for _, q := range qs {
		levels = append(levels, q.Difficulty)
	}
	if want := []model.Difficulty{"easy", "easy", "medium", "hard", "hard"}; !slices.Equal(levels, want) {
		t.Errorf("without shuffle the order is %v, want easiest first %v", levels, want)
	}
}

func TestStartExamRandomWithoutShuffle(t *testing.T) {
	s := newImportStore(t)
	examStore(t, s, 6, 0, 0)
	h := &Handler{store: s, config: model.ExamConfig{NumQuestions: 2, Shuffle: false}}
	sets := map[string]bool{}
	for i := range 20 {
		sets[fmt.Sprint(questionIDs(sessionQuestions(t, s, startedSession(t, h, student(int64(100+i)), "t"))))] = true
	}
	if len(sets) < 2 {
		t.Error("with shuffle off every student got the same questions; the set must still be random")
	}
}

func TestStartExamNeedsTopic(t *testing.T) {
	s := newImportStore(t)
	examStore(t, s, 2, 0, 0)
	h := &Handler{store: s, config: model.ExamConfig{}}
	for _, topic := range []string{"", "other"} {
		rec := startExam(t, h, student(7), topic)
		if topic == "" && rec.Code != http.StatusBadRequest {
			t.Errorf("no topic: status %d, want 400", rec.Code)
		}
		if topic == "other" && rec.Code != http.StatusBadRequest {
			t.Errorf("unknown topic: status %d, want 400", rec.Code)
		}
	}
	if sessions, _ := s.ListSessionsByUser(7); len(sessions) != 0 {
		t.Errorf("sessions = %v, want none", sessions)
	}
}

func indexPage(t *testing.T, h *Handler, user *model.User) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleIndex(rec, withUser(httptest.NewRequest(http.MethodGet, "/", nil), user))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d", rec.Code)
	}
	return rec.Body.String()
}

func TestIndexShowsDistributionAndContinue(t *testing.T) {
	s := newImportStore(t)
	examStore(t, s, 3, 3, 3)
	short := plainQ("Lonely question", "u", true)
	short.Difficulty = "hard"
	insertQ(t, s, short)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 2, "medium": 2, "hard": 1}}}

	body := indexPage(t, h, student(7))
	if !strings.Contains(body, "Each exam: 2 easy, 2 medium and 1 hard questions (5 in total).") {
		t.Errorf("exam size missing")
	}
	if !strings.Contains(body, `<option value="t">t</option>`) {
		t.Errorf("topic t not offered")
	}
	if !strings.Contains(body, `<option value="u" disabled>u — not enough easy questions: 2 needed, 0 available.`) {
		t.Errorf("topic u not shown as unavailable with the reason")
	}

	id := startedSession(t, h, student(7), "t")
	body = indexPage(t, h, student(7))
	if !strings.Contains(body, "Unfinished exams") || !strings.Contains(body, fmt.Sprintf(`href="/exam/%d">Continue`, id)) {
		t.Errorf("no Continue link for the exam in progress")
	}
	if strings.Contains(body, `<option value="t"`) {
		t.Errorf("topic t offered again while its exam is in progress")
	}

	if err := s.UpdateSessionStatus(id, model.StatusGraded); err != nil {
		t.Fatal(err)
	}
	body = indexPage(t, h, student(7))
	if !strings.Contains(body, "you have already taken this exam") {
		t.Errorf("finished topic not marked as taken")
	}
}

func TestIndexKeepsMatchingQuestionsInDifferentTopics(t *testing.T) {
	s := newImportStore(t)
	if _, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam"}); err != nil {
		t.Fatal(err)
	}
	// Normalized duplicates are allowed across topics. Each topic still
	// has a complete exam; only repeats inside one draw should be removed.
	for topic, text := range map[string]string{"t": "What is energy?", "u": "WHAT IS ENERGY!"} {
		insertQ(t, s, plainQ(text, topic, true))
	}
	for _, config := range []model.ExamConfig{
		{Selection: model.Distribution{"easy": 1}},
		{NumQuestions: 1},
	} {
		h := &Handler{store: s, config: config}
		body := indexPage(t, h, student(7))
		for _, topic := range []string{"t", "u"} {
			if !strings.Contains(body, fmt.Sprintf(`<option value="%s">%s</option>`, topic, topic)) {
				t.Errorf("topic %s must be available in config %+v", topic, config)
			}
		}
	}
}
