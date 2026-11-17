package store

import "fmt"

// RecoverInterruptedSessions makes unfinished final grading retryable after
// the exam server restarts. It changes only the status: the saved variant,
// answers, submission timestamp and scores are retained. Call it once before
// serving requests, with no other exam server using this database. Opening a
// Store, for example for export or another connection, must not invoke it.
func (s *Store) RecoverInterruptedSessions() (int64, error) {
	res, err := s.db.Exec(`UPDATE exam_sessions SET status = 'in_progress' WHERE status IN ('submitted', 'grading')`)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted sessions: %w", err)
	}
	return res.RowsAffected()
}
