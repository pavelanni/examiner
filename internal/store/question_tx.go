package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

// QuestionTx is a transaction for importing questions. It satisfies
// questionimport.Tx. Use it only inside InQuestionTx.
type QuestionTx struct {
	tx *sql.Tx
}

// InQuestionTx runs fn in one transaction and commits if fn returns nil.
// _txlock=immediate in the DSN makes BEGIN take the write lock, so lookups
// inside fn cannot race with other writers. fn must not use the Store
// directly: with :memory: a second connection is a different database.
func (s *Store) InQuestionTx(ctx context.Context, fn func(*QuestionTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("question tx: begin: %w", err)
	}
	// Roll back on every exit but a commit, including a panic in fn:
	// a leaked BEGIN IMMEDIATE would block all other writers.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&QuestionTx{tx: tx}); err != nil {
		return fmt.Errorf("question tx: %w", err)
	}
	committed = true
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("question tx: commit: %w", err)
	}
	return nil
}

const questionColumns = `id, course_id, text, text_norm, difficulty, topic, rubric, model_answer, max_points, owner_id, published`

// scanQuestion reads one row selected with questionColumns.
func scanQuestion(row interface{ Scan(...any) error }) (model.Question, error) {
	var q model.Question
	var owner sql.NullInt64
	err := row.Scan(&q.ID, &q.CourseID, &q.Text, &q.TextNorm, &q.Difficulty, &q.Topic, &q.Rubric, &q.ModelAnswer, &q.MaxPoints,
		&owner, &q.Published)
	q.OwnerID = owner.Int64
	return q, err
}

// nullOwner stores OwnerID 0 as NULL.
func nullOwner(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (t *QuestionTx) findOne(where string, args ...any) (*model.Question, error) {
	q, err := scanQuestion(t.tx.QueryRow(`SELECT `+questionColumns+` FROM questions WHERE `+where+` ORDER BY id LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// FindByTopicAndTextNorm returns the active question with this identity,
// or nil if there is none. idx_questions_active_identity allows at most one.
func (t *QuestionTx) FindByTopicAndTextNorm(courseID int64, topic, textNorm string) (*model.Question, error) {
	return t.findOne(`course_id = ? AND topic = ? AND text_norm = ? AND archived = 0`, courseID, topic, textNorm)
}

// Insert stores a new question. Unlike Store.InsertQuestion it does not
// ignore conflicts: a duplicate fails and rolls back the import.
func (t *QuestionTx) Insert(q model.Question) (int64, error) {
	if q.TextNorm == "" {
		q.TextNorm = model.NormalizeQuestionText(q.Text)
	}
	res, err := t.tx.Exec(
		`INSERT INTO questions (course_id, text, text_norm, difficulty, topic, rubric, model_answer, max_points,
		                        owner_id, published, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		q.CourseID, q.Text, q.TextNorm, q.Difficulty, q.Topic, q.Rubric, q.ModelAnswer, q.MaxPoints,
		nullOwner(q.OwnerID), q.Published, time.Now(), time.Now(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Update changes an active question by ID. It returns sql.ErrNoRows if no
// active row matched: archived questions stay as sessions saw them.
func (t *QuestionTx) Update(q model.Question) error {
	if q.TextNorm == "" {
		q.TextNorm = model.NormalizeQuestionText(q.Text)
	}
	res, err := t.tx.Exec(
		`UPDATE questions
		 SET text = ?, text_norm = ?, difficulty = ?, topic = ?, rubric = ?, model_answer = ?, max_points = ?,
		     published = ?, updated_at = ?
		 WHERE id = ? AND archived = 0`,
		q.Text, q.TextNorm, q.Difficulty, q.Topic, q.Rubric, q.ModelAnswer, q.MaxPoints, q.Published, time.Now(), q.ID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteUnused deletes ownerID's active question with this topic and exact
// text unless a question_thread references it. Another owner's question
// and another topic's question with the same text are kept.
func (t *QuestionTx) DeleteUnused(courseID, ownerID int64, topic, text string) error {
	_, err := t.tx.Exec(`DELETE FROM questions
		WHERE course_id = ? AND owner_id = ? AND topic = ? AND text = ? AND archived = 0
		  AND NOT EXISTS (
		      SELECT 1 FROM question_threads WHERE question_threads.question_id = questions.id
		  )`, courseID, ownerID, topic, text)
	return err
}

// SetImportedFileHash records the hash of an imported file.
func (t *QuestionTx) SetImportedFileHash(name, hash string) error {
	now := time.Now()
	_, err := t.tx.Exec(
		`INSERT INTO imported_files (path, hash, imported_at) VALUES (?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET hash = ?, imported_at = ?`,
		name, hash, now, hash, now,
	)
	return err
}

// DeleteImportedFileHash removes the record of an imported file.
func (t *QuestionTx) DeleteImportedFileHash(name string) error {
	_, err := t.tx.Exec(`DELETE FROM imported_files WHERE path = ?`, name)
	return err
}

// UserDisplayName returns the user's display name, or "" for an unknown ID.
func (t *QuestionTx) UserDisplayName(id int64) (string, error) {
	var name string
	err := t.tx.QueryRow(`SELECT display_name FROM users WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}
