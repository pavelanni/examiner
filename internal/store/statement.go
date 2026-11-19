package store

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

// ExamStatement selects submitted attempts without exposing student or session IDs.
func (s *Store) ExamStatement(filter model.StatementFilter) (model.Statement, error) {
	var result model.Statement
	tx, err := s.db.Begin()
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRow(`SELECT value FROM exam_metadata WHERE key = 'subject'`).Scan(&result.Subject)
	if err != nil && err != sql.ErrNoRows {
		return result, err
	}

	query := `SELECT COALESCE(u.display_name, ''), COALESCE(u.group_name, ''),
		CASE WHEN s.topic_known = 1 AND s.topic != '' THEN s.topic ELSE b.name END,
		s.submitted_at, s.started_at,
		CASE WHEN s.status = 'reviewed' THEN g.final_grade END,
		CASE WHEN s.status = 'reviewed' AND g.final_grade IS NOT NULL THEN COALESCE(r.display_name, '') ELSE '' END,
		g.reviewed_at
		FROM exam_sessions s
		JOIN exam_blueprints b ON b.id = s.blueprint_id
		LEFT JOIN users u ON u.id = s.student_id
		LEFT JOIN grades g ON g.session_id = s.id
		LEFT JOIN users r ON r.id = g.reviewed_by
		WHERE s.submitted_at IS NOT NULL
		  AND s.status IN ('submitted', 'grading', 'grading_failed', 'graded', 'reviewed')`
	var args []any
	if filter.Topic != nil {
		query += " AND s.topic_known = 1 AND s.topic = ?"
		args = append(args, *filter.Topic)
	}
	if filter.Status != "" {
		query += " AND s.status = ?"
		args = append(args, filter.Status)
	}
	// The driver also stores legacy Go timestamps that SQLite date functions
	// cannot parse. Compare scanned time.Time values after applying SQL filters.
	query += " ORDER BY s.id DESC"
	rows, err := tx.Query(query, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var row model.StatementRow
		var started time.Time
		var grade sql.NullFloat64
		var reviewed sql.NullTime
		if err := rows.Scan(&row.StudentName, &row.GroupName, &row.ExamName,
			&row.SubmittedAt, &started, &grade, &row.Reviewer, &reviewed); err != nil {
			_ = rows.Close()
			return result, err
		}
		if filter.From != nil && started.Before(*filter.From) {
			continue
		}
		if filter.Until != nil && !started.Before(*filter.Until) {
			continue
		}
		if grade.Valid {
			if grade.Float64 < 0 || grade.Float64 > 100 {
				_ = rows.Close()
				return result, fmt.Errorf("confirmed grade is outside 0..100")
			}
			row.FinalGrade = &grade.Float64
		}
		if grade.Valid && reviewed.Valid {
			row.ReviewedAt = &reviewed.Time
		}
		result.Rows = append(result.Rows, row)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return result, readErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	sort.SliceStable(result.Rows, func(i, j int) bool {
		return result.Rows[i].SubmittedAt.After(result.Rows[j].SubmittedAt)
	})
	return result, tx.Commit()
}
