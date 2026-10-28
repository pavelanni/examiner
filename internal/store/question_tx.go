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
	q, err := scanQuestion(t.tx.QueryRow(`SELECT `+questionColumns+` FROM questions WHERE `+where+` LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// FindByTopicAndTextNorm returns a question with this topic and normalized
// text, or nil if there is none.
func (t *QuestionTx) FindByTopicAndTextNorm(topic, textNorm string) (*model.Question, error) {
	return t.findOne(`topic = ? AND text_norm = ?`, topic, textNorm)
}

// FindByCourseAndText returns the question with exactly this text in the
// course, or nil. It mirrors the idx_questions_course_text unique index.
func (t *QuestionTx) FindByCourseAndText(courseID int64, text string) (*model.Question, error) {
	return t.findOne(`course_id = ? AND text = ?`, courseID, text)
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

// Update changes a question by ID. It returns sql.ErrNoRows if no row matched.
func (t *QuestionTx) Update(q model.Question) error {
	if q.TextNorm == "" {
		q.TextNorm = model.NormalizeQuestionText(q.Text)
	}
	res, err := t.tx.Exec(
		`UPDATE questions
		 SET text = ?, text_norm = ?, difficulty = ?, topic = ?, rubric = ?, model_answer = ?, max_points = ?,
		     published = ?, updated_at = ?
		 WHERE id = ?`,
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

// DeleteUnusedByTexts deletes ownerID's questions whose text is in oldTexts
// but not in keepTexts and that no question_thread references. Another
// owner's question is never deleted, even if its text is in oldTexts.
func (t *QuestionTx) DeleteUnusedByTexts(courseID, ownerID int64, oldTexts, keepTexts []string) error {
	if len(oldTexts) == 0 {
		return nil
	}
	args := append([]any{courseID, ownerID}, stringsToAny(oldTexts)...)
	var notIn string
	if len(keepTexts) > 0 {
		notIn = "AND text NOT IN (" + placeholders(len(keepTexts)) + ")"
		args = append(args, stringsToAny(keepTexts)...)
	}
	_, err := t.tx.Exec(`DELETE FROM questions
		WHERE course_id = ?
		  AND owner_id = ?
		  AND text IN (`+placeholders(len(oldTexts))+`)
		  `+notIn+`
		  AND NOT EXISTS (
		      SELECT 1 FROM question_threads WHERE question_threads.question_id = questions.id
		  )`, args...)
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

// UserDisplayName returns the user's display name, or "" for an unknown ID.
func (t *QuestionTx) UserDisplayName(id int64) (string, error) {
	var name string
	err := t.tx.QueryRow(`SELECT display_name FROM users WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}
