package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
