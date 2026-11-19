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

// Each claim increments the attempt number, which fences workers from expired leases.
type EvaluationJob struct {
	ID, SessionID, ThreadID int64
	Feedback                bool
	Attempt                 int
	RetryDeadline           int64 // Unix milliseconds, zero before the first retry
}

// Workers renew long-running calls. A crashed process stops renewing and a new
// worker can claim the job, even if the service restarted before lease expiry.
const EvaluationLease = 30 * time.Second

const expiredClaim = `unixepoch(claimed_at) <= unixepoch('now') - ?`

// ClaimEvaluation gives waiting feedback priority over final grading.
func (s *Store) ClaimEvaluation(feedbackOnly bool) (*EvaluationJob, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var job EvaluationJob
	query := `SELECT j.id,t.session_id,t.id,j.kind='feedback',j.attempts+1,j.retry_deadline FROM evaluation_jobs j
		JOIN question_threads t ON t.id=j.thread_id JOIN exam_sessions s ON s.id=t.session_id
		WHERE j.state='pending' AND j.ready_at<=? AND (j.claimed_at IS NULL OR ` + expiredClaim + `)
		AND ((j.kind='grade' AND s.status IN ('submitted','grading')) OR (j.kind='feedback' AND s.status='in_progress'))`
	if feedbackOnly {
		query += ` AND j.kind='feedback'`
	}
	err = tx.QueryRow(query+` ORDER BY j.kind,j.id LIMIT 1`, time.Now().UnixMilli(), int64(EvaluationLease/time.Second)).Scan(
		&job.ID, &job.SessionID, &job.ThreadID, &job.Feedback, &job.Attempt, &job.RetryDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE evaluation_jobs SET claimed_at=CURRENT_TIMESTAMP,attempts=attempts+1 WHERE id=?`, job.ID); err != nil {
		return nil, err
	}
	if !job.Feedback {
		if _, err := tx.Exec(`UPDATE exam_sessions SET status='grading' WHERE id=? AND status='submitted'`, job.SessionID); err != nil {
			return nil, err
		}
	}
	return &job, tx.Commit()
}

const activeClaim = `id=? AND attempts=? AND state='pending' AND claimed_at IS NOT NULL`

func (s *Store) RenewEvaluation(job EvaluationJob) (bool, error) {
	res, err := s.db.Exec(`UPDATE evaluation_jobs SET claimed_at=CURRENT_TIMESTAMP WHERE `+activeClaim, job.ID, job.Attempt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n != 0, err
}

func (s *Store) ReleaseEvaluation(job EvaluationJob) error {
	_, err := s.db.Exec(`UPDATE evaluation_jobs SET claimed_at=NULL WHERE `+activeClaim, job.ID, job.Attempt)
	return err
}

// finishEvaluation checks the claim in the same write transaction as its output.
func (s *Store) finishEvaluation(job EvaluationJob, fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM evaluation_jobs WHERE `+activeClaim+`)`, job.ID, job.Attempt).Scan(&exists); err != nil {
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

// FinishGrading stages a successful score. The same claim cannot publish twice.
func (s *Store) FinishGrading(job EvaluationJob, score model.QuestionScore) error {
	return s.finishEvaluation(job, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE evaluation_jobs SET state='graded',score=?,feedback=?,claimed_at=NULL,last_error='' WHERE id=?`,
			score.LLMScore, score.LLMFeedback, job.ID); err != nil {
			return err
		}
		return settleGrading(tx, job.SessionID)
	})
}

// FailGrading either schedules the provider's next allowed attempt or records a
// terminal error. Retry timing and diagnostic state survive a process restart.
func (s *Store) FailGrading(job EvaluationJob, reason string, retryAt, deadline int64) error {
	return s.finishEvaluation(job, func(tx *sql.Tx) error {
		if retryAt != 0 {
			_, err := tx.Exec(`UPDATE evaluation_jobs SET last_error=?, claimed_at=NULL,ready_at=?,retry_deadline=? WHERE id=?`,
				reason, retryAt, deadline, job.ID)
			return err
		}
		if _, err := tx.Exec(`UPDATE evaluation_jobs SET state='failed',last_error=?,claimed_at=NULL WHERE id=?`, reason, job.ID); err != nil {
			return err
		}
		return settleGrading(tx, job.SessionID)
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
	return nil
}
