package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

var msk = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		panic(err)
	}
	return loc
}()

// addReviewSession creates a session with the given status and started_at
// (stored exactly as given, so tests can also write non-UTC values).
func addReviewSession(t *testing.T, s *Store, status model.SessionStatus, started time.Time) int64 {
	t.Helper()
	bpID, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "T"})
	if err != nil {
		t.Fatalf("CreateBlueprint: %v", err)
	}
	id, err := s.CreateSession(bpID, 1, nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE exam_sessions SET status = ?, started_at = ? WHERE id = ?`, status, started, id); err != nil {
		t.Fatalf("set session fields: %v", err)
	}
	return id
}

func reviewIDs(rows []ReviewRow) []int64 {
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, msk) }

func TestListSessionsForReview(t *testing.T) {
	s := newTestStore(t)

	// All times are Moscow time; they are stored in UTC.
	at := func(d, h, m, sec int) time.Time { return time.Date(2026, 11, d, h, m, sec, 0, msk).UTC() }
	early := addReviewSession(t, s, model.StatusGraded, at(3, 0, 0, 0))      // first second of Nov 3
	late := addReviewSession(t, s, model.StatusReviewed, at(3, 23, 59, 59)) // last second of Nov 3
	night := addReviewSession(t, s, model.StatusGraded, at(3, 23, 30, 0))   // 20:30 UTC
	next := addReviewSession(t, s, model.StatusGraded, at(4, 0, 0, 0))      // first second of Nov 4
	prev := addReviewSession(t, s, model.StatusReviewed, at(2, 23, 59, 59)) // last second of Nov 2
	_ = addReviewSession(t, s, model.StatusInProgress, at(3, 12, 0, 0))     // never listed
	_ = addReviewSession(t, s, model.StatusSubmitted, at(3, 12, 0, 0))      // never listed
	sameA := addReviewSession(t, s, model.StatusGraded, at(1, 10, 0, 0))
	sameB := addReviewSession(t, s, model.StatusGraded, at(1, 10, 0, 0)) // same start, higher id

	tests := []struct {
		name string
		f    ReviewFilter
		want []int64
	}{
		{"all, newest first, ties by id desc", ReviewFilter{},
			[]int64{next, late, night, early, prev, sameB, sameA}},
		{"graded only", ReviewFilter{Statuses: []model.SessionStatus{model.StatusGraded}},
			[]int64{next, night, early, sameB, sameA}},
		{"reviewed only", ReviewFilter{Statuses: []model.SessionStatus{model.StatusReviewed}},
			[]int64{late, prev}},
		{"one day includes both ends", ReviewFilter{From: day(2026, 11, 3), To: day(2026, 11, 4)},
			[]int64{late, night, early}},
		{"period from-to", ReviewFilter{From: day(2026, 11, 2), To: day(2026, 11, 4)},
			[]int64{late, night, early, prev}},
		{"from only", ReviewFilter{From: day(2026, 11, 4)}, []int64{next}},
		{"to only", ReviewFilter{To: day(2026, 11, 2)}, []int64{sameB, sameA}},
		{"empty range", ReviewFilter{From: day(2026, 11, 3), To: day(2026, 11, 3)}, nil},
		{"status and date", ReviewFilter{
			Statuses: []model.SessionStatus{model.StatusReviewed},
			From:     day(2026, 11, 3), To: day(2026, 11, 4),
		}, []int64{late}},
		{"page 1", ReviewFilter{Limit: 3}, []int64{next, late, night}},
		{"page 3", ReviewFilter{Limit: 3, Offset: 6}, []int64{sameA}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, total, err := s.ListSessionsForReview(tt.f)
			if err != nil {
				t.Fatalf("ListSessionsForReview: %v", err)
			}
			if got := reviewIDs(rows); !equalIDs(got, tt.want) {
				t.Errorf("ids = %v, want %v", got, tt.want)
			}
			if tt.f.Limit == 0 && total != len(tt.want) {
				t.Errorf("total = %d, want %d", total, len(tt.want))
			}
			if tt.f.Limit > 0 && total != 7 {
				t.Errorf("total = %d, want 7 (all matches, not the page)", total)
			}
		})
	}
}

func TestListSessionsForReviewInvalidRange(t *testing.T) {
	s := newTestStore(t)
	_, _, err := s.ListSessionsForReview(ReviewFilter{From: day(2026, 11, 4), To: day(2026, 11, 3)})
	if !errors.Is(err, ErrInvalidDateRange) {
		t.Fatalf("err = %v, want ErrInvalidDateRange", err)
	}
}

func TestListSessionsForReviewGrades(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	noGrade := addReviewSession(t, s, model.StatusGraded, now.Add(-2*time.Hour))
	llmOnly := addReviewSession(t, s, model.StatusGraded, now.Add(-time.Hour))
	final := addReviewSession(t, s, model.StatusReviewed, now)
	if err := s.UpsertGrade(model.Grade{SessionID: llmOnly, LLMGrade: 70}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGrade(model.Grade{SessionID: final, LLMGrade: 80}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeGrade(final, 90, 1); err != nil {
		t.Fatal(err)
	}

	rows, _, err := s.ListSessionsForReview(ReviewFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]ReviewRow{}
	for _, r := range rows {
		got[r.ID] = r
	}
	if r := got[noGrade]; r.LLMGrade != nil || r.FinalGrade != nil {
		t.Errorf("no grade: got llm=%v final=%v, want nil, nil", r.LLMGrade, r.FinalGrade)
	}
	if r := got[llmOnly]; r.LLMGrade == nil || *r.LLMGrade != 70 || r.FinalGrade != nil {
		t.Errorf("llm only: got llm=%v final=%v", r.LLMGrade, r.FinalGrade)
	}
	if r := got[final]; r.LLMGrade == nil || *r.LLMGrade != 80 || r.FinalGrade == nil || *r.FinalGrade != 90 {
		t.Errorf("final: got llm=%v final=%v", r.LLMGrade, r.FinalGrade)
	}
}

// Sessions written before #52 carry the writer's offset (and sometimes a
// monotonic suffix); migration converts them to UTC so date filters work.
func TestMigrateSessionTimesToUTC(t *testing.T) {
	s := newTestStore(t)

	local := time.Date(2020, 11, 3, 23, 30, 0, 0, msk) // 20:30 UTC
	id := addReviewSession(t, s, model.StatusGraded, local)
	mono := addReviewSession(t, s, model.StatusGraded, time.Now()) // has "m=+..." suffix
	if _, err := s.db.Exec(`UPDATE exam_sessions SET submitted_at = ? WHERE id = ?`, local, id); err != nil {
		t.Fatal(err)
	}

	if err := s.migrateSessionTimesToUTC(); err != nil {
		t.Fatalf("migrateSessionTimesToUTC: %v", err)
	}

	for _, sid := range []int64{id, mono} {
		var started, submitted *string
		if err := s.db.QueryRow(`SELECT CAST(started_at AS TEXT), CAST(submitted_at AS TEXT) FROM exam_sessions WHERE id = ?`, sid).
			Scan(&started, &submitted); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(*started, utcTimeSuffix) {
			t.Errorf("session %d started_at = %q, want UTC", sid, *started)
		}
		if submitted != nil && !strings.HasSuffix(*submitted, utcTimeSuffix) {
			t.Errorf("session %d submitted_at = %q, want UTC", sid, *submitted)
		}
	}

	sess, err := s.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.StartedAt.Equal(local) {
		t.Errorf("started_at = %v, want the same instant as %v", sess.StartedAt, local)
	}

	// The converted session lands on Nov 3 in Moscow time.
	rows, _, err := s.ListSessionsForReview(ReviewFilter{From: day(2020, 11, 3), To: day(2020, 11, 4)})
	if err != nil {
		t.Fatal(err)
	}
	if got := reviewIDs(rows); !equalIDs(got, []int64{id}) {
		t.Errorf("ids = %v, want [%d]", got, id)
	}
}
