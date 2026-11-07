package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/pavelanni/examiner/internal/model"
)

// ConfigureQuestionCount reconciles a distribution's size with prepared metadata.
// Existing attempts must all have that size; validation and update are atomic.
func (s *Store) ConfigureQuestionCount(n int) error {
	if n <= 0 {
		return errors.New("question count must be positive")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID int64
	var actual int
	err = tx.QueryRow(`SELECT s.id, COUNT(t.id)
		FROM exam_sessions s LEFT JOIN question_threads t ON t.session_id = s.id
		GROUP BY s.id HAVING COUNT(t.id) != ? ORDER BY s.id LIMIT 1`, n).Scan(&sessionID, &actual)
	if err == nil {
		return fmt.Errorf("configured question count %d conflicts with session %d (%d questions); use a separate exam database", n, sessionID, actual)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(`INSERT INTO exam_metadata (key, value) VALUES ('num_questions', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.Itoa(n))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SetMetadata upserts a key-value pair in the exam_metadata table.
func (s *Store) SetMetadata(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO exam_metadata (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = ?`,
		key, value, value,
	)
	return err
}

// GetMetadata returns the value for a metadata key.
// Returns empty string and nil error if the key is missing.
func (s *Store) GetMetadata(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM exam_metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetExamInfo stores all ExamInfo fields as metadata rows.
func (s *Store) SetExamInfo(info model.ExamInfo) error {
	pairs := []struct{ k, v string }{
		{"exam_id", info.ExamID},
		{"subject", info.Subject},
		{"date", info.Date},
		{"prompt_variant", info.PromptVariant},
		{"num_questions", strconv.Itoa(info.NumQuestions)},
	}
	for _, p := range pairs {
		if err := s.SetMetadata(p.k, p.v); err != nil {
			return err
		}
	}
	return nil
}

// GetExamInfo reads all ExamInfo fields from metadata.
func (s *Store) GetExamInfo() (model.ExamInfo, error) {
	var info model.ExamInfo
	var err error

	if info.ExamID, err = s.GetMetadata("exam_id"); err != nil {
		return info, err
	}
	if info.Subject, err = s.GetMetadata("subject"); err != nil {
		return info, err
	}
	if info.Date, err = s.GetMetadata("date"); err != nil {
		return info, err
	}
	if info.PromptVariant, err = s.GetMetadata("prompt_variant"); err != nil {
		return info, err
	}
	nq, err := s.GetMetadata("num_questions")
	if err != nil {
		return info, err
	}
	if nq != "" {
		info.NumQuestions, err = strconv.Atoi(nq)
		if err != nil {
			return info, err
		}
	}
	return info, nil
}
