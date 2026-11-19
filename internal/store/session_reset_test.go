package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/sqlitedb"
)

func resetFixture(t *testing.T, s *Store) (sessionID, threadID, teacherID int64) {
	t.Helper()
	teacherID = createUser(t, s, "reset-teacher", model.UserRoleTeacher)
	studentID := createUser(t, s, "reset-student", model.UserRoleStudent)
	bp, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Reset exam", MaxFollowups: 3})
	if err != nil {
		t.Fatal(err)
	}
	q := insertTestQuestion(t, s, "Original question", "easy", "t")
	sess, created, err := s.StartSession(bp, studentID, "t", []int64{q}, false)
	if err != nil || !created {
		t.Fatalf("start: %v, created %v", err, created)
	}
	threads, err := s.GetThreadsForSession(sess.ID)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads: %v, %v", threads, err)
	}
	threadID = threads[0].ID
	if _, err := s.AddMessage(model.Message{ThreadID: threadID, Role: model.RoleStudent, Content: "Saved answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMessage(model.Message{ThreadID: threadID, Role: model.RoleLLM, Content: "Saved feedback", Followup: "Why?"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertScore(model.QuestionScore{ThreadID: threadID, LLMScore: 7, LLMFeedback: "Saved grade"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTeacherScore(threadID, 8, "Saved teacher comment"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGrade(model.Grade{SessionID: sess.ID, LLMGrade: 70}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeGrade(sess.ID, 80, teacherID); err != nil {
		t.Fatal(err)
	}
	return sess.ID, threadID, teacherID
}

func TestResetSessionPreservesHistoryAndAllowsRetake(t *testing.T) {
	for _, status := range []model.SessionStatus{model.StatusInProgress, model.StatusSubmitted, model.StatusGrading, model.StatusGraded, model.StatusReviewed} {
		t.Run(string(status), func(t *testing.T) {
			s := newTestStore(t)
			id, _, actor := resetFixture(t, s)
			if err := s.UpdateSessionStatus(id, status); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetSessionView(id)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now().UTC()
			if err := s.ResetSession(id, actor, "  Connection lost  "); err != nil {
				t.Fatal(err)
			}
			after, err := s.GetSessionView(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.Session.Status != model.StatusAnnulled || after.Session.ResetBy == nil || *after.Session.ResetBy != actor || after.Session.ResetAt == nil || after.Session.ResetAt.Before(started) || after.Session.ResetReason != "Connection lost" || after.ResetByName != "reset-teacher" {
				t.Fatalf("missing audit: %+v", after)
			}
			if !reflect.DeepEqual(before.Threads, after.Threads) || !reflect.DeepEqual(before.Grade, after.Grade) || !before.Session.StartedAt.Equal(after.Session.StartedAt) || !reflect.DeepEqual(before.Session.SubmittedAt, after.Session.SubmittedAt) {
				t.Fatal("reset changed historical records")
			}
			if err := s.ResetSession(id, actor, "Overwrite audit"); !errors.Is(err, ErrSessionAnnulled) {
				t.Fatalf("repeat reset: %v", err)
			}
			unchanged, err := s.GetSessionView(id)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, unchanged) {
				t.Fatal("repeated reset overwrote original audit")
			}
			if found, err := s.FindExamSession(before.Session.BlueprintID, before.Session.StudentID, "t", false); err != nil || found != nil {
				t.Fatalf("cancelled attempt still selected: %+v, %v", found, err)
			}
			replacement := insertTestQuestion(t, s, "New question", "easy", "t")
			fresh, created, err := s.StartSession(before.Session.BlueprintID, before.Session.StudentID, "t", []int64{replacement}, false)
			if err != nil || !created || fresh.ID == id {
				t.Fatalf("retake: %+v, %v, %v", fresh, created, err)
			}
			ts, err := s.GetThreadsForSession(fresh.ID)
			if err != nil || len(ts) != 1 || ts[0].QuestionID != replacement {
				t.Fatalf("new draw: %+v, %v", ts, err)
			}
			old, _, err := s.GetSessionWithBlueprint(id)
			if err != nil || !reflect.DeepEqual(old, after.Session) {
				t.Fatalf("joined lookup lost audit: %+v, %v", old, err)
			}
			history, err := s.ListSessionsByUser(before.Session.StudentID)
			if err != nil || len(history) != 2 || history[1].Status != model.StatusAnnulled {
				t.Fatalf("history: %+v, %v", history, err)
			}
		})
	}
}

func TestResetSessionRejectsAllLateWrites(t *testing.T) {
	s := newTestStore(t)
	id, thread, actor := resetFixture(t, s)
	if err := s.ResetSession(id, actor, ""); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSessionView(id)
	if err != nil {
		t.Fatal(err)
	}
	writes := map[string]func() error{
		"status":         func() error { return s.UpdateSessionStatus(id, model.StatusGraded) },
		"capacity retry": func() error { return s.UpdateSessionStatus(id, model.StatusInProgress) },
		"thread status":  func() error { return s.UpdateThreadStatus(thread, model.ThreadCompleted) },
		"LLM reply": func() error {
			_, err := s.AddMessage(model.Message{ThreadID: thread, Role: model.RoleLLM, Content: "Late", Followup: "Late?"})
			return err
		},
		"score":         func() error { return s.UpsertScore(model.QuestionScore{ThreadID: thread, LLMScore: 0}) },
		"teacher score": func() error { return s.UpdateTeacherScore(thread, 0, "Late") },
		"grade":         func() error { return s.UpsertGrade(model.Grade{SessionID: id, LLMGrade: 0}) },
		"finalize":      func() error { return s.FinalizeGrade(id, 0, actor) },
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			if err := write(); !errors.Is(err, ErrSessionAnnulled) {
				t.Fatalf("late write: %v", err)
			}
		})
	}
	if _, err := s.RecoverInterruptedSessions(); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetSessionView(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("a late write modified cancelled history")
	}
}

func TestResetSessionAuthorizationAndReason(t *testing.T) {
	s := newTestStore(t)
	id, _, teacher := resetFixture(t, s)
	student := createUser(t, s, "student-actor", model.UserRoleStudent)
	inactive := createUser(t, s, "inactive-actor", model.UserRoleTeacher)
	if err := s.ToggleUserActive(inactive); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []int64{student, inactive, 99999} {
		if err := s.ResetSession(id, actor, ""); !errors.Is(err, ErrResetForbidden) {
			t.Fatalf("actor %d: %v", actor, err)
		}
	}
	if err := s.ResetSession(99999, teacher, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing session: %v", err)
	}
	if err := s.ResetSession(id, teacher, strings.Repeat("я", model.MaxResetReasonRunes+1)); !errors.Is(err, ErrResetReasonTooLong) {
		t.Fatalf("long reason: %v", err)
	}
	sess, err := s.GetSession(id)
	if err != nil || sess.Status != model.StatusInProgress || sess.ResetAt != nil {
		t.Fatalf("invalid reset changed session: %+v, %v", sess, err)
	}
	admin := createUser(t, s, "admin-actor", model.UserRoleAdmin)
	if err := s.ResetSession(id, admin, strings.Repeat("я", model.MaxResetReasonRunes)); err != nil {
		t.Fatalf("Unicode boundary: %v", err)
	}
}

func TestResetSessionMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", sqlitedb.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE exam_sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT, blueprint_id INTEGER NOT NULL,
  student_id INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'in_progress',
  started_at DATETIME NOT NULL, submitted_at DATETIME);
  INSERT INTO exam_sessions (blueprint_id,student_id,status,started_at) VALUES (1,1,'graded',CURRENT_TIMESTAMP);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	legacy, err := s.GetSession(1)
	if err != nil || legacy.ResetAt != nil || legacy.ResetBy != nil || legacy.ResetReason != "" || legacy.Status != model.StatusGraded {
		t.Fatalf("legacy migration: %+v, %v", legacy, err)
	}
	actor := createUser(t, s, "migration-teacher", model.UserRoleTeacher)
	if err := s.ResetSession(1, actor, "Legacy retake"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	sess, err := reopened.GetSession(1)
	if err != nil || sess.Status != model.StatusAnnulled || sess.ResetReason != "Legacy retake" || sess.ResetBy == nil || *sess.ResetBy != actor || sess.ResetAt == nil {
		t.Fatalf("persistent audit: %+v, %v", sess, err)
	}
}

func TestResetSessionIndependentConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, thread, actor := resetFixture(t, s)
	other, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errorsCh := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); <-start; errorsCh <- s.ResetSession(id, actor, "Concurrent reset") }()
	go func() {
		defer wg.Done()
		<-start
		errorsCh <- other.UpsertScore(model.QuestionScore{ThreadID: thread, LLMScore: 9})
	}()
	close(start)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil && !errors.Is(err, ErrSessionAnnulled) {
			t.Fatal(err)
		}
	}
	old, err := other.GetSession(id)
	if err != nil || old.Status != model.StatusAnnulled {
		t.Fatalf("reset lost: %+v, %v", old, err)
	}
	threads, err := other.GetThreadsForSession(id)
	if err != nil {
		t.Fatal(err)
	}
	fresh, created, err := other.StartSession(old.BlueprintID, old.StudentID, old.Topic, []int64{threads[0].QuestionID}, false)
	if err != nil || !created || fresh.ID == id {
		t.Fatalf("new slot unavailable: %+v, %v, %v", fresh, created, err)
	}
	if err := s.UpdateSessionStatus(id, model.StatusInProgress); !errors.Is(err, ErrSessionAnnulled) {
		t.Fatalf("old writer resurrected reset: %v", err)
	}
}

func TestExportRetainsAnnulledHistory(t *testing.T) {
	s := newTestStore(t)
	id, _, actor := resetFixture(t, s)
	if err := s.ResetSession(id, actor, "Retake allowed"); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSessionView(id)
	if err != nil {
		t.Fatal(err)
	}
	fresh, created, err := s.StartSession(before.Session.BlueprintID, before.Session.StudentID, "t", []int64{before.Threads[0].Question.ID}, false)
	if err != nil || !created {
		t.Fatalf("retake: %v, %v", created, err)
	}
	results, err := s.ExportAllSessions()
	if err != nil || len(results) != 2 {
		t.Fatalf("export: %+v, %v", results, err)
	}
	if results[0].Status != model.StatusAnnulled || results[0].SessionNumber != 1 || len(results[0].Questions) != 1 || len(results[0].Questions[0].Conversation) != 2 || results[0].LLMGrade == nil || *results[0].LLMGrade != 70 {
		t.Fatalf("reset history missing from export: %+v", results[0])
	}
	if results[1].Status != fresh.Status || results[1].SessionNumber != 2 {
		t.Fatalf("new attempt lost: %+v", results[1])
	}
}
