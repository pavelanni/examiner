package handler

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// newExamStore creates an isolated, localized exam database.
func newExamStore(t *testing.T) *store.Store {
	t.Helper()
	if err := i18n.Init("en"); err != nil {
		t.Fatal(err)
	}
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// withExamUser adds an authenticated test user and English localizer.
func withExamUser(r *http.Request, u *model.User) *http.Request {
	return r.WithContext(i18n.WithLocalizer(model.ContextWithUser(r.Context(), u), i18n.NewLocalizer("en")))
}

// examQuestion builds a valid question fixture.
func examQuestion(text, topic string) model.Question {
	return model.Question{CourseID: 1, Text: text, Topic: topic, Difficulty: model.DifficultyEasy, Rubric: "r", ModelAnswer: "a", MaxPoints: 5}
}

// insertExamQuestion inserts a fixture and fails on a skipped or invalid row.
func insertExamQuestion(t *testing.T, s *store.Store, q model.Question) int64 {
	t.Helper()
	id, err := s.InsertQuestion(q)
	if err != nil || id == 0 {
		t.Fatalf("insert question: %d, %v", id, err)
	}
	return id
}

// examStore creates a blueprint and the requested difficulty pools.
func examStore(t *testing.T, s *store.Store, easy, medium, hard int) {
	t.Helper()
	if _, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Exam", MaxFollowups: 3}); err != nil {
		t.Fatal(err)
	}
	for level, n := range map[model.Difficulty]int{"easy": easy, "medium": medium, "hard": hard} {
		for i := range n {
			q := examQuestion(fmt.Sprintf("%s question %d", level, i), "t")
			q.Difficulty = level
			insertExamQuestion(t, s, q)
		}
	}
}

// student builds a student identity for exam requests.
func student(id int64) *model.User {
	return &model.User{ID: id, Username: fmt.Sprint("s", id), DisplayName: "S", Role: model.UserRoleStudent}
}

// startExam submits an explicit topic to the exam-start handler.
func startExam(t *testing.T, h *Handler, user *model.User, topic string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/exam/start", strings.NewReader(url.Values{"topic": {topic}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.handleStartExam(rec, withExamUser(req, user))
	return rec
}

// startedSession returns the new session ID from a successful redirect.
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

// sessionQuestions loads the saved questions in their displayed order.
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

// questionIDs extracts saved question IDs for comparison.
func questionIDs(qs []model.Question) []int64 {
	var ids []int64
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	return ids
}

// TestStartExamExactDistribution checks 50 sessions for exact counts and varied question sets.
func TestStartExamExactDistribution(t *testing.T) {
	s := newExamStore(t)
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

// TestStartExamShortage prevents partial sessions when a difficulty pool is too small.
func TestStartExamShortage(t *testing.T) {
	s := newExamStore(t)
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

// TestStartExamOrder checks easiest-first order with shuffle disabled.
func TestStartExamOrder(t *testing.T) {
	s := newExamStore(t)
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

// indexPage renders the home page for a given identity.
func indexPage(t *testing.T, h *Handler, user *model.User) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleIndex(rec, withExamUser(httptest.NewRequest(http.MethodGet, "/", nil), user))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d", rec.Code)
	}
	return rec.Body.String()
}

// TestIndexShowsDistributionAndAvailability checks exam size and shortage explanations.
func TestIndexShowsDistributionAndAvailability(t *testing.T) {
	s := newExamStore(t)
	examStore(t, s, 3, 3, 3)
	short := examQuestion("Lonely question", "u")
	short.Difficulty = "hard"
	insertExamQuestion(t, s, short)
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

}

// TestIndexKeepsMatchingQuestionsInDifferentTopics keeps independent topic banks available.
func TestIndexKeepsMatchingQuestionsInDifferentTopics(t *testing.T) {
	s := newExamStore(t)
	if _, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam"}); err != nil {
		t.Fatal(err)
	}
	for i, topic := range []string{"t", "u"} {
		q := examQuestion("What is energy?", topic)
		// The existing uniqueness rule is scoped to a course. These are
		// separate banks with matching text, each able to supply an exam.
		q.CourseID = int64(i + 1)
		insertExamQuestion(t, s, q)
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

// TestStartExamLegacyCompatibility preserves old attempt creation and flags before the #46 change.
func TestStartExamLegacyCompatibility(t *testing.T) {
	s := newExamStore(t)
	examStore(t, s, 4, 3, 2)
	h := &Handler{store: s, config: model.ExamConfig{NumQuestions: 2, Difficulty: "easy", Shuffle: false}}
	first := startedSession(t, h, student(7), "t")
	second := startedSession(t, h, student(7), "t")
	if first == second {
		t.Fatal("legacy attempt creation must remain unchanged until #46")
	}
	want := questionIDs(sessionQuestions(t, s, first))
	if got := questionIDs(sessionQuestions(t, s, second)); len(got) != 2 || !slices.Equal(got, want) {
		t.Fatalf("legacy selection with shuffle=false changed: %v, want %v", got, want)
	}
	for _, q := range sessionQuestions(t, s, second) {
		if q.Difficulty != model.DifficultyEasy {
			t.Fatalf("legacy difficulty filter ignored: %s", q.Difficulty)
		}
	}
}

// TestEmptyExamTopic distinguishes an explicit empty bank from all other topics.
func TestEmptyExamTopic(t *testing.T) {
	s := newExamStore(t)
	if _, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam"}); err != nil {
		t.Fatal(err)
	}
	insertExamQuestion(t, s, examQuestion("Empty topic easy", ""))
	hard := examQuestion("Other topic hard", "other")
	hard.Difficulty = model.DifficultyHard
	insertExamQuestion(t, s, hard)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 1, "hard": 1}}}
	if rec := startExam(t, h, student(7), ""); rec.Code != http.StatusConflict {
		t.Fatalf("empty topic borrowed questions from another bank: status %d", rec.Code)
	}
	hard.Text, hard.Topic = "Empty topic hard", ""
	insertExamQuestion(t, s, hard)
	id := startedSession(t, h, student(7), "")
	for _, q := range sessionQuestions(t, s, id) {
		if q.Topic != "" {
			t.Fatalf("question escaped the selected topic: %+v", q)
		}
	}
	body := indexPage(t, h, student(7))
	if !strings.Contains(body, `<option value="">No topic</option>`) || strings.Contains(body, `<select id="topic" name="topic" required`) {
		t.Fatal("the browser must allow the explicit empty topic")
	}
	rec := httptest.NewRecorder()
	h.handleStartExam(rec, withExamUser(httptest.NewRequest(http.MethodPost, "/exam/start", nil), student(8)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing selection must remain distinct from an empty topic: %d", rec.Code)
	}
	h.config.Topic = "other"
	if rec := startExam(t, h, student(8), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty topic bypassed configured-topic restriction: %d", rec.Code)
	}
}

// TestIndexEmptyBankShowsDistribution keeps the exam size and shortage visible.
func TestIndexEmptyBankShowsDistribution(t *testing.T) {
	s := newExamStore(t)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 2, "medium": 2, "hard": 1}, Topic: "missing"}}
	body := indexPage(t, h, student(7))
	for _, want := range []string{"Each exam: 2 easy, 2 medium and 1 hard questions (5 in total).", "missing", "not enough hard questions: 1 needed, 0 available."} {
		if !strings.Contains(body, want) {
			t.Errorf("empty-bank page lacks %q", want)
		}
	}
}

// TestDrawQuestionsShuffle catches ignoring the order flag for exact distributions.
func TestDrawQuestionsShuffle(t *testing.T) {
	s := newExamStore(t)
	examStore(t, s, 1, 1, 1)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 1, "medium": 1, "hard": 1}}}
	ordered, err := h.drawQuestions("t", rand.New(rand.NewPCG(42, 42)))
	if err != nil {
		t.Fatal(err)
	}
	h.config.Shuffle = true
	shuffled, err := h.drawQuestions("t", rand.New(rand.NewPCG(42, 42)))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(questionIDs(ordered), questionIDs(shuffled)) {
		t.Fatal("fixed seed must produce an order different from the unshuffled variant")
	}
	got := questionIDs(shuffled)
	slices.Sort(got)
	want := questionIDs(ordered)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("shuffle changed composition: %v, want %v", got, want)
	}
}

// TestTopicAvailabilityUsesDistribution prevents pre-deduplication hiding a valid bank.
func TestTopicAvailabilityUsesDistribution(t *testing.T) {
	s := newExamStore(t)
	if _, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam"}); err != nil {
		t.Fatal(err)
	}
	easy := examQuestion("Same text", "t")
	insertExamQuestion(t, s, easy)
	hard := easy
	hard.CourseID, hard.Difficulty = 2, model.DifficultyHard
	insertExamQuestion(t, s, hard)
	h := &Handler{store: s, config: model.ExamConfig{Selection: model.Distribution{"easy": 0, "hard": 1}}}
	body := indexPage(t, h, student(7))
	if !strings.Contains(body, `action="/exam/start"`) {
		t.Fatal("a duplicate in an unrequested difficulty hid a complete topic")
	}
	id := startedSession(t, h, student(7), "t")
	if qs := sessionQuestions(t, s, id); len(qs) != 1 || qs[0].Difficulty != model.DifficultyHard {
		t.Fatalf("incorrect drawn distribution: %+v", qs)
	}
}
