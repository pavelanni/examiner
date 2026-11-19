package store

import (
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

// TestExamStatementApprovalAndFilters covers submitted attempts, zero grades,
// stale approvals, legacy topics, date boundaries and rows without student IDs.
func TestExamStatementApprovalAndFilters(t *testing.T) {
	s := newTestStore(t)
	student, err := s.CreateUser(model.User{Username: "student", DisplayName: "Иванов; Иван", GroupName: "Г-1", PasswordHash: "x", Role: model.UserRoleStudent, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := s.CreateUser(model.User{Username: "teacher", DisplayName: "Преподаватель", PasswordHash: "x", Role: model.UserRoleTeacher, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMetadata("subject", "Физика"); err != nil {
		t.Fatal(err)
	}
	bp, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Legacy exam"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 16, 21, 0, 0, 0, time.UTC)
	submitted := start.Add(2 * time.Hour)
	cases := []struct {
		status    model.SessionStatus
		submitted bool
		grade     any
		known     bool
	}{
		{model.StatusInProgress, false, nil, true},
		{model.StatusAnnulled, true, 100, true},
		{model.StatusGraded, true, nil, true},
		{model.StatusReviewed, true, 0, true},
		{model.StatusGraded, true, 99, true}, // stale final grade must not escape
		{model.StatusReviewed, true, nil, true},
		{model.StatusSubmitted, true, nil, false},
	}
	for _, tc := range cases {
		id, err := s.CreateSession(bp, student, nil)
		if err != nil {
			t.Fatal(err)
		}
		var date any
		if tc.submitted {
			date = submitted
		}
		if _, err := s.db.Exec(`UPDATE exam_sessions SET topic = 'Механика', topic_known = ?, status = ?,
			started_at = ?, submitted_at = ? WHERE id = ?`, tc.known, tc.status, start, date, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO grades(session_id, llm_grade, final_grade, reviewed_by, reviewed_at)
			VALUES (?, 88, ?, ?, ?)`, id, tc.grade, reviewer, submitted.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.ExamStatement(model.StatementFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Subject != "Физика" || len(report.Rows) != 5 {
		t.Fatalf("unexpected report: %+v", report)
	}
	confirmed := 0
	for _, row := range report.Rows {
		if row.StudentName != "Иванов; Иван" || row.GroupName != "Г-1" || !row.SubmittedAt.Equal(submitted) {
			t.Fatalf("incorrect student or completion date: %+v", row)
		}
		if row.FinalGrade != nil {
			confirmed++
			if *row.FinalGrade != 0 || row.Reviewer != "Преподаватель" || row.ReviewedAt == nil {
				t.Fatalf("zero grade lost: %+v", row)
			}
		} else if row.Reviewer != "" || row.ReviewedAt != nil {
			t.Fatalf("unconfirmed approval leaked: %+v", row)
		}
	}
	if confirmed != 1 || report.Rows[0].ExamName != "Legacy exam" {
		t.Fatalf("confirmed=%d, legacy row=%+v", confirmed, report.Rows[0])
	}
	topic := "Механика"
	until := start.AddDate(0, 0, 1)
	filter := model.StatementFilter{Topic: &topic, Status: model.StatusGraded, From: &start, Until: &until}
	filtered, err := s.ExamStatement(filter)
	if err != nil || len(filtered.Rows) != 2 {
		t.Fatalf("filtered report: %+v, %v", filtered, err)
	}
	filter.From = &until
	filter.Until = nil
	filtered, err = s.ExamStatement(filter)
	if err != nil || len(filtered.Rows) != 0 {
		t.Fatalf("outside date range: %+v, %v", filtered, err)
	}
}

// TestGroupNameMigration verifies existing accounts and all user read paths.
func TestGroupNameMigration(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec("ALTER TABLE users DROP COLUMN group_name"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateUser(model.User{Username: "grouped", GroupName: "Г-2", PasswordHash: "x", Role: model.UserRoleStudent, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	byID, err := s.GetUserByID(id)
	if err != nil || byID.GroupName != "Г-2" {
		t.Fatalf("by ID: %+v, %v", byID, err)
	}
	byName, err := s.GetUserByUsername("grouped")
	if err != nil || byName.GroupName != "Г-2" {
		t.Fatalf("by username: %+v, %v", byName, err)
	}
	users, err := s.ListUsers()
	if err != nil || len(users) != 1 || users[0].GroupName != "Г-2" {
		t.Fatalf("list: %+v, %v", users, err)
	}
}

// TestGroupMigrationPreservesExistingUsers exercises an actual pre-group schema.
func TestGroupMigrationPreservesExistingUsers(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateUser(model.User{Username: "legacy", DisplayName: "Старый пользователь", PasswordHash: "x", Role: model.UserRoleStudent, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE users DROP COLUMN group_name"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	user, err := s.GetUserByID(id)
	if err != nil || user == nil || user.DisplayName != "Старый пользователь" || user.GroupName != "" {
		t.Fatalf("legacy user = %+v, %v", user, err)
	}
}

// TestStatementExcludesReopenedAttempt covers the queue-timeout recovery path,
// which returns an attempt to in_progress while retaining submitted_at.
func TestStatementExcludesReopenedAttempt(t *testing.T) {
	s := newTestStore(t)
	bp, err := s.CreateBlueprint(model.ExamBlueprint{CourseID: 1, Name: "Exam"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateSession(bp, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []model.SessionStatus{
		model.StatusSubmitted, model.StatusGrading, model.StatusGradingFailed, model.StatusGraded, model.StatusReviewed,
	} {
		if err := s.UpdateSessionStatus(id, status); err != nil {
			t.Fatal(err)
		}
		report, err := s.ExamStatement(model.StatementFilter{})
		if err != nil || len(report.Rows) != 1 {
			t.Fatalf("status %s: rows=%d, error=%v", status, len(report.Rows), err)
		}
	}
	if err := s.UpdateSessionStatus(id, model.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	session, err := s.GetSession(id)
	if err != nil || session.SubmittedAt == nil {
		t.Fatalf("expected retained submission date: session=%+v, error=%v", session, err)
	}
	for _, filter := range []model.StatementFilter{{}, {Status: model.StatusInProgress}} {
		report, err := s.ExamStatement(filter)
		if err != nil || len(report.Rows) != 0 {
			t.Fatalf("reopened attempt exported: rows=%d, error=%v", len(report.Rows), err)
		}
	}
}
