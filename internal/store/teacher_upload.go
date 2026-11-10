package store

import (
	"fmt"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

// SaveTeacherQuestions applies all database changes in one transaction. The
// caller publishes the prepared file before commit and restores it on error.
func (s *Store) SaveTeacherQuestions(questions []model.Question, oldTexts []string, oldName, name, hash string, publish func() error) error {
	if len(questions) == 0 {
		return fmt.Errorf("no questions to save")
	}
	courseID := questions[0].CourseID
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	keep := make([]string, 0, len(questions))
	for _, q := range questions {
		if q.CourseID != courseID {
			return fmt.Errorf("question belongs to a different course")
		}
		_, err := tx.Exec(`INSERT INTO questions (course_id, text, difficulty, topic, rubric, model_answer, max_points)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(course_id, text) DO UPDATE SET difficulty = excluded.difficulty,
				topic = excluded.topic, rubric = excluded.rubric,
				model_answer = excluded.model_answer, max_points = excluded.max_points`,
			q.CourseID, q.Text, q.Difficulty, q.Topic, q.Rubric, q.ModelAnswer, q.MaxPoints)
		if err != nil {
			return fmt.Errorf("save question: %w", err)
		}
		keep = append(keep, q.Text)
	}
	if err := deleteUnusedQuestionsByTexts(tx, courseID, oldTexts, keep); err != nil {
		return fmt.Errorf("delete old questions: %w", err)
	}
	if oldName != "" && oldName != name {
		if _, err := tx.Exec(`DELETE FROM imported_files WHERE path = ?`, oldName); err != nil {
			return fmt.Errorf("remove old import hash: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO imported_files (path, hash, imported_at) VALUES (?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET hash = excluded.hash, imported_at = excluded.imported_at`,
		name, hash, time.Now()); err != nil {
		return fmt.Errorf("save import hash: %w", err)
	}
	if err := publish(); err != nil {
		return fmt.Errorf("publish question file: %w", err)
	}
	return tx.Commit()
}
