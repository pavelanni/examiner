package store

// enforceActiveQuestionIdentity keeps the oldest active row for each identity.
// Archived rows retain their IDs and fields for sessions that reference them.
func (s *Store) enforceActiveQuestionIdentity() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`ALTER TABLE questions ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`); err != nil && !isAlterDuplicate(err) {
		return err
	}
	if _, err := tx.Exec(`UPDATE questions SET archived = 1
		WHERE archived = 0 AND id NOT IN (
			SELECT MIN(id) FROM questions WHERE archived = 0
			GROUP BY course_id, topic, text_norm
		)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_questions_active_identity
		ON questions(course_id, topic, text_norm) WHERE archived = 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_questions_topic_text_norm`); err != nil {
		return err
	}
	return tx.Commit()
}
