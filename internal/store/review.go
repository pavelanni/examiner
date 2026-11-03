package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

// ErrInvalidDateRange is returned when a review filter's From is after To.
var ErrInvalidDateRange = errors.New("invalid date range: from is after to")

// ReviewStatuses are the session statuses shown in the teacher review list
// when the filter does not name any.
var ReviewStatuses = []model.SessionStatus{model.StatusGraded, model.StatusReviewed}

// ReviewFilter selects sessions for the teacher review list.
type ReviewFilter struct {
	// Statuses to include. Empty means ReviewStatuses.
	Statuses []model.SessionStatus
	// From and To bound started_at as the half-open range [From, To).
	// A zero value leaves that side open. The caller computes day
	// boundaries in the configured time zone.
	From, To time.Time
	// Limit is the page size (0 means no limit); Offset skips rows.
	Limit, Offset int
}

// ReviewRow is one session in the teacher review list. It carries no
// student identity: the list is anonymous (FR5).
type ReviewRow struct {
	model.ExamSession
	LLMGrade   *float64
	FinalGrade *float64
}

// SetSessionTimestampsForTest sets status, started_at, and submitted_at on an
// existing session for deterministic tests.
func (s *Store) SetSessionTimestampsForTest(id int64, status model.SessionStatus, startedAt time.Time, submittedAt *time.Time) error {
	_, err := s.db.Exec(
		`UPDATE exam_sessions SET status = ?, started_at = ?, submitted_at = ? WHERE id = ?`,
		status, startedAt.UTC(), submittedAt, id,
	)
	return err
}

// ListSessionsForReview returns one page of sessions matching f, newest
// first (started_at DESC, id DESC), and the total number of matches.
func (s *Store) ListSessionsForReview(f ReviewFilter) ([]ReviewRow, int, error) {
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return nil, 0, ErrInvalidDateRange
	}

	statuses := f.Statuses
	if len(statuses) == 0 {
		statuses = ReviewStatuses
	}
	conds := []string{"s.status IN (?" + strings.Repeat(", ?", len(statuses)-1) + ")"}
	args := make([]any, 0, len(statuses)+4)
	for _, st := range statuses {
		args = append(args, string(st))
	}
	// started_at is stored as UTC text (see migrateSessionTimesToUTC), so
	// comparing it with UTC bounds as text orders it correctly.
	if !f.From.IsZero() {
		conds = append(conds, "s.started_at >= ?")
		args = append(args, f.From.UTC())
	}
	if !f.To.IsZero() {
		conds = append(conds, "s.started_at < ?")
		args = append(args, f.To.UTC())
	}
	where := " WHERE " + strings.Join(conds, " AND ")

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM exam_sessions s`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count review sessions: %w", err)
	}

	query := `SELECT s.id, s.blueprint_id, s.student_id, s.status, s.started_at, s.submitted_at,
		g.llm_grade, g.final_grade
		FROM exam_sessions s LEFT JOIN grades g ON g.session_id = s.id` +
		where + ` ORDER BY s.started_at DESC, s.id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, f.Offset)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list review sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ReviewRow
	for rows.Next() {
		var r ReviewRow
		if err := rows.Scan(&r.ID, &r.BlueprintID, &r.StudentID, &r.Status, &r.StartedAt, &r.SubmittedAt,
			&r.LLMGrade, &r.FinalGrade); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// utcTimeSuffix ends every time the driver writes for a UTC time.Time
// without a monotonic reading (time.Time.String format).
const utcTimeSuffix = " +0000 UTC"

// migrateSessionTimesToUTC rewrites started_at and submitted_at values that
// are not stored in UTC. The driver stores time.Time as text with the
// writer's offset (and sometimes a monotonic "m=+..." suffix), which SQLite
// cannot compare across offsets. New values are written in UTC; this
// converts old ones once. It is a no-op when every value is already UTC.
func (s *Store) migrateSessionTimesToUTC() error {
	for _, col := range []string{"started_at", "submitted_at"} {
		rows, err := s.db.Query(
			`SELECT id, ` + col + ` FROM exam_sessions
			 WHERE ` + col + ` IS NOT NULL AND CAST(` + col + ` AS TEXT) NOT LIKE '%` + utcTimeSuffix + `'`)
		if err != nil {
			return fmt.Errorf("find non-UTC %s: %w", col, err)
		}
		type fix struct {
			id int64
			t  time.Time
		}
		var fixes []fix
		for rows.Next() {
			var f fix
			if err := rows.Scan(&f.id, &f.t); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan %s: %w", col, err)
			}
			fixes = append(fixes, f)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()

		for _, f := range fixes {
			if _, err := s.db.Exec(`UPDATE exam_sessions SET `+col+` = ? WHERE id = ?`, f.t.UTC(), f.id); err != nil {
				return fmt.Errorf("convert %s to UTC: %w", col, err)
			}
		}
	}
	return nil
}

