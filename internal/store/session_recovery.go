package store

// RecoverInterruptedSessions releases work left by the previous server. Only
// legacy submissions with no durable jobs are reopened for manual resubmission.
// Run once with exclusive server ownership, never from the store constructor/export.
func (s *Store) RecoverInterruptedSessions() (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE evaluation_jobs SET claimed_at=NULL WHERE claimed_at IS NOT NULL AND score IS NULL`); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE exam_sessions SET status='in_progress' WHERE status IN ('submitted','grading')
		AND NOT EXISTS(SELECT 1 FROM evaluation_jobs j JOIN question_threads t ON t.id=j.thread_id WHERE t.session_id=exam_sessions.id AND j.kind='grade')`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
