package store

import (
	"database/sql"
	"errors"

	"github.com/pavelanni/examiner/internal/model"
)

// migrateEvaluationJobs also upgrades jobs written by the original background
// worker: a non-NULL score was its marker for an already graded thread.
func (s *Store) migrateEvaluationJobs() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, column := range []string{
		`state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','graded','failed'))`,
		`attempts INTEGER NOT NULL DEFAULT 0`,
		`generation INTEGER NOT NULL DEFAULT 0`,
		`last_error TEXT NOT NULL DEFAULT ''`,
		`ready_at INTEGER NOT NULL DEFAULT 0`,
		`retry_deadline INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := tx.Exec("ALTER TABLE evaluation_jobs ADD COLUMN " + column); err != nil && !isAlterDuplicate(err) {
			return err
		}
	}
	_, err = tx.Exec(`
		UPDATE evaluation_jobs SET state='graded',claimed_at=NULL WHERE score IS NOT NULL AND state='pending';
		DROP INDEX IF EXISTS idx_evaluation_ready;
		CREATE INDEX IF NOT EXISTS idx_evaluation_pending ON evaluation_jobs(kind,id) WHERE state='pending';
		DROP TRIGGER IF EXISTS requeue_edited_question;
		CREATE TRIGGER IF NOT EXISTS requeue_unpublished_evaluation
		AFTER UPDATE OF text, rubric, model_answer, max_points ON questions
		WHEN NEW.text != OLD.text OR NEW.rubric != OLD.rubric
			OR NEW.model_answer != OLD.model_answer OR NEW.max_points != OLD.max_points
		BEGIN
			INSERT OR REPLACE INTO evaluation_jobs(thread_id,kind)
			SELECT j.thread_id,j.kind FROM evaluation_jobs j
			JOIN question_threads t ON t.id=j.thread_id
			JOIN exam_sessions s ON s.id=t.session_id
			WHERE t.question_id=NEW.id AND j.state!='failed'
			  AND (j.claimed_at IS NOT NULL OR j.state='graded' OR j.attempts>0)
			  AND s.status IN ('in_progress','submitted','grading','grading_failed');
		END;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// GradingProgress returns the status of final grades for the staff review page.
func (s *Store) GradingProgress(sessionID int64) (map[int64]model.GradingProgress, error) {
	rows, err := s.db.Query(`SELECT thread_id,state,attempts,last_error FROM evaluation_jobs
		WHERE kind='grade' AND thread_id IN (`+sessionThreads+`)`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	progress := make(map[int64]model.GradingProgress)
	for rows.Next() {
		var threadID int64
		var item model.GradingProgress
		if err := rows.Scan(&threadID, &item.State, &item.Attempts, &item.LastError); err != nil {
			return nil, err
		}
		progress[threadID] = item
	}
	return progress, rows.Err()
}

var ErrGradingNotFailed = errors.New("session is not awaiting a grading retry")

// RetryGrading opens a fresh retry budget only for failed threads. Replacing
// their job IDs fences delayed workers. The student's submission stays closed.
func (s *Store) RetryGrading(sessionID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status model.SessionStatus
	if err := tx.QueryRow(`SELECT status FROM exam_sessions WHERE id=?`, sessionID).Scan(&status); err != nil {
		return err
	}
	if status != model.StatusGradingFailed {
		return ErrGradingNotFailed
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO evaluation_jobs(thread_id,kind)
		SELECT thread_id,kind FROM evaluation_jobs WHERE kind='grade' AND state='failed'
		AND thread_id IN (`+sessionThreads+`)`, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE exam_sessions SET status='grading' WHERE id=?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// settleGrading publishes only after every thread succeeds. On failure other
// threads may finish, so a later staff retry can reuse all successful work.
func settleGrading(tx *sql.Tx, sessionID int64) error {
	var pending, failed int
	if err := tx.QueryRow(`SELECT COUNT(*) FILTER (WHERE state='pending'), COUNT(*) FILTER (WHERE state='failed')
		FROM evaluation_jobs WHERE kind='grade' AND thread_id IN (`+sessionThreads+`)`, sessionID).Scan(&pending, &failed); err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	if failed > 0 {
		_, err := tx.Exec(`UPDATE exam_sessions SET status='grading_failed' WHERE id=?`, sessionID)
		return err
	}
	return publishGrades(tx, sessionID)
}
