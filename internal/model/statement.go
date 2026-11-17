package model

import "time"

// StatementRow contains only the public columns of one submitted attempt.
// A nil grade means that the result has not been approved.
type StatementRow struct {
	StudentName string
	GroupName   string
	ExamName    string
	SubmittedAt time.Time
	FinalGrade  *float64
	Reviewer    string
	ReviewedAt  *time.Time
}

// Statement contains a subject and a consistent snapshot of submitted attempts.
type Statement struct {
	Subject string
	Rows    []StatementRow
}

// StatementFilter uses half-open time boundaries for exam start dates.
type StatementFilter struct {
	Topic  *string
	Status SessionStatus
	From   *time.Time
	Until  *time.Time
}
