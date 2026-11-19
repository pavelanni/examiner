package store

import (
	"database/sql"
	"time"
)

// RecoverInterruptedSessions releases expired leases and turns legacy submissions
// without durable jobs into queued work. Only the exam server invokes it.
func (s *Store) RecoverInterruptedSessions() (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE evaluation_jobs SET claimed_at=NULL WHERE state='pending' AND `+expiredClaim, int64(EvaluationLease/time.Second)); err != nil {
		return 0, err
	}
	rows, err := tx.Query(`SELECT id FROM exam_sessions WHERE status IN ('submitted','grading')
		AND NOT EXISTS(SELECT 1 FROM evaluation_jobs j JOIN question_threads t ON t.id=j.thread_id
		WHERE t.session_id=exam_sessions.id AND j.kind='grade')`)
	if err != nil {
		return 0, err
	}
	var legacy []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		legacy = append(legacy, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range legacy {
		if err := recoverLegacySubmission(tx, id); err != nil {
			return 0, err
		}
	}
	return int64(len(legacy)), tx.Commit()
}

func recoverLegacySubmission(tx *sql.Tx, id int64) error {
	// The synchronous grader used this exact prefix for provider errors and
	// recorded a synthetic zero. Genuine zero scores are already graded.
	if _, err := tx.Exec(`DELETE FROM question_scores WHERE thread_id IN (`+sessionThreads+`)
		AND llm_score=0 AND llm_feedback GLOB 'Grading error: *' AND teacher_score IS NULL AND teacher_comment=''`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO evaluation_jobs(thread_id,kind,state,score,feedback)
		SELECT t.id,'grade',CASE WHEN sc.id IS NULL THEN 'pending' ELSE 'graded' END,
			sc.llm_score,COALESCE(sc.llm_feedback,'')
		FROM question_threads t LEFT JOIN question_scores sc ON sc.thread_id=t.id
			AND NOT (sc.llm_score=0 AND sc.llm_feedback GLOB 'Grading error: *')
		WHERE t.session_id=? ORDER BY t.position,t.id`, id); err != nil {
		return err
	}
	return settleGrading(tx, id)
}
