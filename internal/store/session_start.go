package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

const sessionColumns = `id, blueprint_id, student_id, topic, topic_known, status, started_at, submitted_at`

type rowScanner interface{ Scan(...any) error }

// scanSession reads one row selected with sessionColumns.
func scanSession(row rowScanner) (model.ExamSession, error) {
	var sess model.ExamSession
	err := row.Scan(&sess.ID, &sess.BlueprintID, &sess.StudentID, &sess.Topic, &sess.TopicKnown, &sess.Status, &sess.StartedAt, &sess.SubmittedAt)
	return sess, err
}

func scanSessions(rows *sql.Rows) ([]model.ExamSession, error) {
	defer func() { _ = rows.Close() }()
	var sessions []model.ExamSession
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

// FindExamSession returns the student's session for an exam (blueprint and
// topic): prefer one in progress, otherwise the latest. With retake set,
// completed attempts are excluded, but submitted/grading attempts are kept
// until their evaluation finishes. It returns nil if there is none.
func (s *Store) FindExamSession(blueprintID, studentID int64, topic string, retake bool) (*model.ExamSession, error) {
	return findExamSession(s.db, blueprintID, studentID, topic, retake)
}

func findExamSession(q queryRower, blueprintID, studentID int64, topic string, retake bool) (*model.ExamSession, error) {
	query := `SELECT ` + sessionColumns + ` FROM exam_sessions WHERE student_id = ? AND blueprint_id = ? AND topic = ? AND topic_known = 1`
	if retake {
		// Grading can return an attempt to in_progress when the LLM queue
		// is busy (#47). A new attempt must not occupy its unique slot.
		query += ` AND status IN ('in_progress', 'submitted', 'grading')`
	}
	query += ` ORDER BY status = 'in_progress' DESC, id DESC LIMIT 1`
	sess, err := scanSession(q.QueryRow(query, studentID, blueprintID, topic))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// StartSession returns the student's session for an exam (blueprint and
// topic), as FindExamSession finds it, or creates one with questionIDs in
// this order. The lookup and the insert run in one write transaction, so
// concurrent starts end up in the same session and a student cannot get a
// second variant (FR3-05). created reports whether a session was created.
func (s *Store) StartSession(blueprintID, studentID int64, topic string, questionIDs []int64, retake bool) (sess model.ExamSession, created bool, err error) {
	// _txlock=immediate takes the write lock here, before the lookup.
	tx, err := s.db.Begin()
	if err != nil {
		return model.ExamSession{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := findExamSession(tx, blueprintID, studentID, topic, retake)
	if err != nil {
		return model.ExamSession{}, false, fmt.Errorf("find session: %w", err)
	}
	if existing != nil {
		return *existing, false, nil
	}
	id, err := createSession(tx, blueprintID, studentID, topic, questionIDs, true)
	if err != nil {
		return model.ExamSession{}, false, err
	}
	sess, err = scanSession(tx.QueryRow(`SELECT `+sessionColumns+` FROM exam_sessions WHERE id = ?`, id))
	if err != nil {
		return model.ExamSession{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ExamSession{}, false, err
	}
	return sess, true, nil
}

// createSession inserts a session in progress and one thread per question,
// numbered in the order given. Every question must still exist.
func createSession(tx *sql.Tx, blueprintID, studentID int64, topic string, questionIDs []int64, topicKnown bool) (int64, error) {
	res, err := tx.Exec(
		`INSERT INTO exam_sessions (blueprint_id, student_id, topic, topic_known, status, started_at) VALUES (?, ?, ?, ?, 'in_progress', ?)`,
		blueprintID, studentID, topic, topicKnown, time.Now(),
	)
	if err != nil {
		return 0, err
	}
	sessionID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for i, qID := range questionIDs {
		res, err := tx.Exec(
			`INSERT INTO question_threads (session_id, question_id, status, position)
			 SELECT ?, id, 'open', ? FROM questions WHERE id = ?`,
			sessionID, i+1, qID,
		)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, fmt.Errorf("question %d does not exist", qID)
		}
	}
	slog.Debug("created session", "id", sessionID, "topic", topic, "questions", len(questionIDs))
	return sessionID, nil
}
