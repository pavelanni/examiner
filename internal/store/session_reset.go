package store

import (
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pavelanni/examiner/internal/model"
)

var (
	// ErrSessionAnnulled means a reset attempt is immutable, including to
	// requests that started before the reset while an LLM call was in flight.
	ErrSessionAnnulled    = errors.New("exam attempt has been reset")
	ErrResetForbidden     = errors.New("only an active teacher or administrator may reset an attempt")
	ErrResetReasonTooLong = errors.New("reset reason is too long")
)

// ResetSession annuls an attempt without deleting its questions, answers or
// grades. The status change frees the active unique slot in the same transaction.
func (s *Store) ResetSession(sessionID, actorID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > model.MaxResetReasonRunes {
		return ErrResetReasonTooLong
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var role model.UserRole
	var active bool
	err = tx.QueryRow(`SELECT role, active FROM users WHERE id = ?`, actorID).Scan(&role, &active)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!active || (role != model.UserRoleTeacher && role != model.UserRoleAdmin))) {
		return ErrResetForbidden
	}
	if err != nil {
		return err
	}
	var status model.SessionStatus
	if err := tx.QueryRow(`SELECT status FROM exam_sessions WHERE id = ?`, sessionID).Scan(&status); err != nil {
		return err
	}
	if status == model.StatusAnnulled {
		return ErrSessionAnnulled
	}
	if _, err := tx.Exec(`UPDATE exam_sessions SET status = 'annulled', reset_at = ?, reset_by = ?, reset_reason = ? WHERE id = ?`,
		time.Now().UTC(), actorID, reason, sessionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("reset exam attempt", "session_id", sessionID, "actor_id", actorID)
	return nil
}

// withMutableSession and withMutableThread serialize each write against reset.
// The SQLite DSN uses BEGIN IMMEDIATE, so the status check and mutation cannot
// race with ResetSession, even through independent Store connections.
func (s *Store) withMutableSession(id int64, mutate func(*sql.Tx) error) error {
	return s.withMutableAttempt(`SELECT status FROM exam_sessions WHERE id = ?`, id, mutate)
}

func (s *Store) withMutableThread(id int64, mutate func(*sql.Tx) error) error {
	return s.withMutableAttempt(`SELECT s.status FROM exam_sessions s JOIN question_threads t ON t.session_id = s.id WHERE t.id = ?`, id, mutate)
}

func (s *Store) withMutableAttempt(query string, id int64, mutate func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status model.SessionStatus
	if err := tx.QueryRow(query, id).Scan(&status); err != nil {
		return err
	}
	if status == model.StatusAnnulled {
		return ErrSessionAnnulled
	}
	if err := mutate(tx); err != nil {
		return err
	}
	return tx.Commit()
}
