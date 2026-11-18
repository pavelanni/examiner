package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

// ErrTurnChanged means the form belongs to an earlier turn or the attempt has closed.
var ErrTurnChanged = errors.New("answer form is no longer current")

// QueueAnswer commits the student's message, its visible state, and the job together.
// The idempotency key is the last message the form displayed (0 on the first turn).
// A retried form can never append an extra answer, even after feedback has arrived.
func (s *Store) QueueAnswer(sessionID, threadID, turn int64, answer string, retry bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var session model.SessionStatus
	var thread model.ThreadStatus
	err = tx.QueryRow(`SELECT s.status, t.status FROM question_threads t JOIN exam_sessions s ON s.id=t.session_id
		WHERE s.id=? AND t.id=?`, sessionID, threadID).Scan(&session, &thread)
	if err != nil {
		return err
	}
	if session != model.StatusInProgress || thread == model.ThreadEvaluating || thread == model.ThreadCompleted {
		return ErrTurnChanged
	}

	var last int64
	var role model.Role
	err = tx.QueryRow(`SELECT id, role FROM messages WHERE thread_id=? ORDER BY id DESC LIMIT 1`, threadID).Scan(&last, &role)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// A legacy unanswered message is retryable too. Retry refers to the saved
	// message ID and never trusts the browser to supply its text again.
	if turn != last || retry != (role == model.RoleStudent) {
		return ErrTurnChanged
	}
	if !retry {
		if _, err := insertMessage(tx, model.Message{ThreadID: threadID, Role: model.RoleStudent, Content: answer}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE question_threads SET status='evaluating' WHERE id=?`, threadID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO evaluation_jobs(thread_id,kind) VALUES (?,'feedback')`, threadID); err != nil {
		return err
	}
	return tx.Commit()
}

const sessionThreads = `SELECT id FROM question_threads WHERE session_id=?`

func discardSessionJobs(tx *sql.Tx, sessionID int64) error {
	_, err := tx.Exec(`DELETE FROM evaluation_jobs WHERE thread_id IN (`+sessionThreads+`)`, sessionID)
	return err
}

// QueueSubmission closes answer entry and snapshots the work for all saved answers.
// Deleting outstanding feedback jobs invalidates even a worker already inside the LLM.
func (s *Store) QueueSubmission(sessionID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE exam_sessions SET status='submitted',submitted_at=? WHERE id=? AND status='in_progress'`, time.Now(), sessionID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTurnChanged
	}
	if err := discardSessionJobs(tx, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE question_threads SET status='answered' WHERE session_id=? AND status='evaluating'`, sessionID); err != nil {
		return err
	}
	res, err = tx.Exec(`INSERT INTO evaluation_jobs(thread_id,kind) SELECT id,'grade' FROM question_threads WHERE session_id=? ORDER BY position,id`, sessionID)
	if err != nil {
		return err
	}
	n, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if err := publishGrades(tx, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EvaluationJob identifies one claim. Its ID is never reused if a student
// submits, retries, or a teacher resets an attempt during an LLM call.
type EvaluationJob struct {
	ID, SessionID, ThreadID int64
	Feedback                bool
}

// ClaimEvaluation gives waiting feedback priority over final grading. One
// worker can be reserved for feedback when the configured pool has spare capacity.
func (s *Store) ClaimEvaluation(feedbackOnly bool) (*EvaluationJob, error) {
	// Let the short claim transaction finish at shutdown. Cancelling BeginTx
	// can discard the only connection to a private SQLite in-memory database.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var job EvaluationJob
	query := `SELECT j.id,t.session_id,t.id,j.kind='feedback' FROM evaluation_jobs j
		JOIN question_threads t ON t.id=j.thread_id WHERE j.claimed_at IS NULL AND j.score IS NULL`
	if feedbackOnly {
		query += ` AND j.kind='feedback'`
	}
	// The ready index orders 'feedback' before 'grade', FIFO within each kind.
	err = tx.QueryRow(query+` ORDER BY j.kind,j.id LIMIT 1`).Scan(&job.ID, &job.SessionID, &job.ThreadID, &job.Feedback)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE evaluation_jobs SET claimed_at=CURRENT_TIMESTAMP WHERE id=?`, job.ID); err != nil {
		return nil, err
	}
	if !job.Feedback {
		if _, err := tx.Exec(`UPDATE exam_sessions SET status='grading' WHERE id=? AND status='submitted'`, job.SessionID); err != nil {
			return nil, err
		}
	}
	return &job, tx.Commit()
}

// ReleaseEvaluation makes a cancelled or interrupted worker's job claimable again.
func (s *Store) ReleaseEvaluation(id int64) error {
	_, err := s.db.Exec(`UPDATE evaluation_jobs SET claimed_at=NULL WHERE id=? AND score IS NULL`, id)
	return err
}

// finishEvaluation checks the claim in the same write transaction as its output.
// A missing job was superseded by a submission/reset while its worker was running.
func (s *Store) finishEvaluation(job EvaluationJob, fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM evaluation_jobs WHERE id=? AND claimed_at IS NOT NULL AND score IS NULL)`, job.ID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishFeedback either publishes one reply (including any follow-up), or makes
// the saved student answer retryable. The consumed job is removed atomically.
func (s *Store) FinishFeedback(job EvaluationJob, reply *model.Message) error {
	return s.finishEvaluation(job, func(tx *sql.Tx) error {
		status := model.ThreadFeedbackFailed
		if reply != nil {
			if _, err := insertMessage(tx, *reply); err != nil {
				return err
			}
			status = model.ThreadCompleted
			if reply.Followup != "" {
				status = model.ThreadAnswered
			}
		}
		if _, err := tx.Exec(`UPDATE question_threads SET status=? WHERE id=?`, status, job.ThreadID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM evaluation_jobs WHERE id=?`, job.ID)
		return err
	})
}

// FinishGrading stages a result until all threads have been graded. A provider
// error reopens the saved attempt for resubmission without publishing partial scores.
func (s *Store) FinishGrading(job EvaluationJob, score *model.QuestionScore) error {
	return s.finishEvaluation(job, func(tx *sql.Tx) error {
		if score == nil {
			if err := discardSessionJobs(tx, job.SessionID); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE exam_sessions SET status='in_progress' WHERE id=?`, job.SessionID)
			return err
		}
		if _, err := tx.Exec(`UPDATE evaluation_jobs SET score=?,feedback=? WHERE id=?`, score.LLMScore, score.LLMFeedback, job.ID); err != nil {
			return err
		}
		var remaining bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM evaluation_jobs WHERE kind='grade' AND score IS NULL AND thread_id IN (`+sessionThreads+`))`, job.SessionID).Scan(&remaining); err != nil {
			return err
		}
		if remaining {
			return nil
		}
		return publishGrades(tx, job.SessionID)
	})
}

func publishGrades(tx *sql.Tx, sessionID int64) error {
	if _, err := tx.Exec(`INSERT INTO question_scores(thread_id,llm_score,llm_feedback)
		SELECT thread_id,score,feedback FROM evaluation_jobs WHERE kind='grade' AND thread_id IN (`+sessionThreads+`)
		ON CONFLICT(thread_id) DO UPDATE SET llm_score=excluded.llm_score,llm_feedback=excluded.llm_feedback`, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO grades(session_id,llm_grade)
		SELECT ?,COALESCE(100.0*SUM(j.score)/NULLIF(SUM(q.max_points),0),0) FROM evaluation_jobs j
		JOIN question_threads t ON j.thread_id=t.id JOIN questions q ON t.question_id=q.id WHERE t.session_id=? AND j.kind='grade'
		ON CONFLICT(session_id) DO UPDATE SET llm_grade=excluded.llm_grade`, sessionID, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE question_threads SET status='completed' WHERE session_id=?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE exam_sessions SET status='graded' WHERE id=?`, sessionID); err != nil {
		return err
	}
	return discardSessionJobs(tx, sessionID)
}
