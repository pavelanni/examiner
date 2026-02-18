package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/pavelanni/examiner/internal/model"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS questions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		course_id INTEGER NOT NULL DEFAULT 1,
		text TEXT NOT NULL,
		difficulty TEXT NOT NULL,
		topic TEXT NOT NULL,
		rubric TEXT NOT NULL DEFAULT '',
		model_answer TEXT NOT NULL DEFAULT '',
		max_points INTEGER NOT NULL DEFAULT 10
	);

	CREATE TABLE IF NOT EXISTS exam_blueprints (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		course_id INTEGER NOT NULL DEFAULT 1,
		name TEXT NOT NULL,
		time_limit INTEGER NOT NULL DEFAULT 0,
		max_followups INTEGER NOT NULL DEFAULT 3
	);

	CREATE TABLE IF NOT EXISTS exam_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		blueprint_id INTEGER NOT NULL,
		student_id INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL DEFAULT 'in_progress',
		started_at DATETIME NOT NULL,
		submitted_at DATETIME,
		FOREIGN KEY (blueprint_id) REFERENCES exam_blueprints(id)
	);

	CREATE TABLE IF NOT EXISTS question_threads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL,
		question_id INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'open',
		FOREIGN KEY (session_id) REFERENCES exam_sessions(id),
		FOREIGN KEY (question_id) REFERENCES questions(id)
	);

	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		thread_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		token_count INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (thread_id) REFERENCES question_threads(id)
	);

	CREATE TABLE IF NOT EXISTS question_scores (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		thread_id INTEGER NOT NULL UNIQUE,
		llm_score REAL NOT NULL DEFAULT 0,
		llm_feedback TEXT NOT NULL DEFAULT '',
		teacher_score REAL,
		teacher_comment TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (thread_id) REFERENCES question_threads(id)
	);

	CREATE TABLE IF NOT EXISTS grades (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL UNIQUE,
		llm_grade REAL NOT NULL DEFAULT 0,
		final_grade REAL,
		reviewed_by INTEGER,
		reviewed_at DATETIME,
		FOREIGN KEY (session_id) REFERENCES exam_sessions(id)
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

// InsertQuestion stores a question.
func (s *Store) InsertQuestion(q model.Question) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO questions (course_id, text, difficulty, topic, rubric, model_answer, max_points)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		q.CourseID, q.Text, q.Difficulty, q.Topic, q.Rubric, q.ModelAnswer, q.MaxPoints,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListQuestions returns all questions.
func (s *Store) ListQuestions() ([]model.Question, error) {
	rows, err := s.db.Query(`SELECT id, course_id, text, difficulty, topic, rubric, model_answer, max_points FROM questions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var questions []model.Question
	for rows.Next() {
		var q model.Question
		if err := rows.Scan(&q.ID, &q.CourseID, &q.Text, &q.Difficulty, &q.Topic, &q.Rubric, &q.ModelAnswer, &q.MaxPoints); err != nil {
			return nil, err
		}
		questions = append(questions, q)
	}
	return questions, rows.Err()
}

// ListQuestionsFiltered returns questions matching the given filters.
// Empty strings mean no filtering on that field.
func (s *Store) ListQuestionsFiltered(difficulty string, topic string) ([]model.Question, error) {
	query := `SELECT id, course_id, text, difficulty, topic, rubric, model_answer, max_points FROM questions WHERE 1=1`
	var args []any
	if difficulty != "" {
		query += ` AND difficulty = ?`
		args = append(args, difficulty)
	}
	if topic != "" {
		query += ` AND topic = ?`
		args = append(args, topic)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var questions []model.Question
	for rows.Next() {
		var q model.Question
		if err := rows.Scan(&q.ID, &q.CourseID, &q.Text, &q.Difficulty, &q.Topic, &q.Rubric, &q.ModelAnswer, &q.MaxPoints); err != nil {
			return nil, err
		}
		questions = append(questions, q)
	}
	return questions, rows.Err()
}

// GetQuestion returns a question by ID.
func (s *Store) GetQuestion(id int64) (model.Question, error) {
	var q model.Question
	err := s.db.QueryRow(
		`SELECT id, course_id, text, difficulty, topic, rubric, model_answer, max_points FROM questions WHERE id = ?`, id,
	).Scan(&q.ID, &q.CourseID, &q.Text, &q.Difficulty, &q.Topic, &q.Rubric, &q.ModelAnswer, &q.MaxPoints)
	return q, err
}

// CreateBlueprint creates an exam blueprint.
func (s *Store) CreateBlueprint(bp model.ExamBlueprint) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO exam_blueprints (course_id, name, time_limit, max_followups) VALUES (?, ?, ?, ?)`,
		bp.CourseID, bp.Name, bp.TimeLimit, bp.MaxFollowups,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetBlueprint returns a blueprint by ID.
func (s *Store) GetBlueprint(id int64) (model.ExamBlueprint, error) {
	var bp model.ExamBlueprint
	err := s.db.QueryRow(
		`SELECT id, course_id, name, time_limit, max_followups FROM exam_blueprints WHERE id = ?`, id,
	).Scan(&bp.ID, &bp.CourseID, &bp.Name, &bp.TimeLimit, &bp.MaxFollowups)
	return bp, err
}

// CreateSession creates an exam session with threads for each question.
func (s *Store) CreateSession(blueprintID int64, questionIDs []int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO exam_sessions (blueprint_id, student_id, status, started_at) VALUES (?, 1, 'in_progress', ?)`,
		blueprintID, time.Now(),
	)
	if err != nil {
		return 0, err
	}
	sessionID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for _, qID := range questionIDs {
		_, err := tx.Exec(
			`INSERT INTO question_threads (session_id, question_id, status) VALUES (?, ?, 'open')`,
			sessionID, qID,
		)
		if err != nil {
			return 0, err
		}
	}

	return sessionID, tx.Commit()
}

// GetSession returns a session by ID.
func (s *Store) GetSession(id int64) (model.ExamSession, error) {
	var sess model.ExamSession
	err := s.db.QueryRow(
		`SELECT id, blueprint_id, student_id, status, started_at, submitted_at FROM exam_sessions WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.BlueprintID, &sess.StudentID, &sess.Status, &sess.StartedAt, &sess.SubmittedAt)
	return sess, err
}

// UpdateSessionStatus updates the session status.
func (s *Store) UpdateSessionStatus(id int64, status model.SessionStatus) error {
	query := `UPDATE exam_sessions SET status = ? WHERE id = ?`
	args := []any{status, id}
	if status == model.StatusSubmitted {
		query = `UPDATE exam_sessions SET status = ?, submitted_at = ? WHERE id = ?`
		now := time.Now()
		args = []any{status, now, id}
	}
	_, err := s.db.Exec(query, args...)
	return err
}

// GetThreadsForSession returns all threads for a session.
func (s *Store) GetThreadsForSession(sessionID int64) ([]model.QuestionThread, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, question_id, status FROM question_threads WHERE session_id = ? ORDER BY id`, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var threads []model.QuestionThread
	for rows.Next() {
		var t model.QuestionThread
		if err := rows.Scan(&t.ID, &t.SessionID, &t.QuestionID, &t.Status); err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// GetThread returns a thread by ID.
func (s *Store) GetThread(id int64) (model.QuestionThread, error) {
	var t model.QuestionThread
	err := s.db.QueryRow(
		`SELECT id, session_id, question_id, status FROM question_threads WHERE id = ?`, id,
	).Scan(&t.ID, &t.SessionID, &t.QuestionID, &t.Status)
	return t, err
}

// UpdateThreadStatus updates the thread status.
func (s *Store) UpdateThreadStatus(id int64, status model.ThreadStatus) error {
	_, err := s.db.Exec(`UPDATE question_threads SET status = ? WHERE id = ?`, status, id)
	return err
}

// AddMessage inserts a message into a thread.
func (s *Store) AddMessage(msg model.Message) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO messages (thread_id, role, content, created_at, token_count) VALUES (?, ?, ?, ?, ?)`,
		msg.ThreadID, msg.Role, msg.Content, time.Now(), msg.TokenCount,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetMessages returns all messages for a thread.
func (s *Store) GetMessages(threadID int64) ([]model.Message, error) {
	rows, err := s.db.Query(
		`SELECT id, thread_id, role, content, created_at, token_count FROM messages WHERE thread_id = ? ORDER BY id`, threadID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []model.Message
	for rows.Next() {
		var m model.Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.CreatedAt, &m.TokenCount); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// CountStudentMessages returns the count of student messages in a thread.
func (s *Store) CountStudentMessages(threadID int64) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE thread_id = ? AND role = 'student'`, threadID,
	).Scan(&count)
	return count, err
}

// UpsertScore inserts or updates a score for a thread.
func (s *Store) UpsertScore(score model.QuestionScore) error {
	_, err := s.db.Exec(
		`INSERT INTO question_scores (thread_id, llm_score, llm_feedback)
		 VALUES (?, ?, ?)
		 ON CONFLICT(thread_id) DO UPDATE SET llm_score = ?, llm_feedback = ?`,
		score.ThreadID, score.LLMScore, score.LLMFeedback, score.LLMScore, score.LLMFeedback,
	)
	return err
}

// GetScore returns the score for a thread.
func (s *Store) GetScore(threadID int64) (*model.QuestionScore, error) {
	var sc model.QuestionScore
	err := s.db.QueryRow(
		`SELECT id, thread_id, llm_score, llm_feedback, teacher_score, teacher_comment
		 FROM question_scores WHERE thread_id = ?`, threadID,
	).Scan(&sc.ID, &sc.ThreadID, &sc.LLMScore, &sc.LLMFeedback, &sc.TeacherScore, &sc.TeacherComment)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sc, err
}

// UpdateTeacherScore updates the teacher's score and comment.
func (s *Store) UpdateTeacherScore(threadID int64, score float64, comment string) error {
	_, err := s.db.Exec(
		`UPDATE question_scores SET teacher_score = ?, teacher_comment = ? WHERE thread_id = ?`,
		score, comment, threadID,
	)
	return err
}

// UpsertGrade inserts or updates the grade for a session.
func (s *Store) UpsertGrade(g model.Grade) error {
	_, err := s.db.Exec(
		`INSERT INTO grades (session_id, llm_grade)
		 VALUES (?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET llm_grade = ?`,
		g.SessionID, g.LLMGrade, g.LLMGrade,
	)
	return err
}

// GetGrade returns the grade for a session.
func (s *Store) GetGrade(sessionID int64) (*model.Grade, error) {
	var g model.Grade
	err := s.db.QueryRow(
		`SELECT id, session_id, llm_grade, final_grade, reviewed_by, reviewed_at
		 FROM grades WHERE session_id = ?`, sessionID,
	).Scan(&g.ID, &g.SessionID, &g.LLMGrade, &g.FinalGrade, &g.ReviewedBy, &g.ReviewedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &g, err
}

// FinalizeGrade sets the final grade after teacher review.
func (s *Store) FinalizeGrade(sessionID int64, finalGrade float64) error {
	now := time.Now()
	_, err := s.db.Exec(
		`UPDATE grades SET final_grade = ?, reviewed_by = 1, reviewed_at = ? WHERE session_id = ?`,
		finalGrade, now, sessionID,
	)
	return err
}

// GetSessionView builds a full view of a session with all threads, messages, and scores.
func (s *Store) GetSessionView(sessionID int64) (*model.SessionView, error) {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	bp, err := s.GetBlueprint(sess.BlueprintID)
	if err != nil {
		return nil, err
	}
	threads, err := s.GetThreadsForSession(sessionID)
	if err != nil {
		return nil, err
	}

	var threadViews []model.ThreadView
	for _, t := range threads {
		q, err := s.GetQuestion(t.QuestionID)
		if err != nil {
			return nil, err
		}
		msgs, err := s.GetMessages(t.ID)
		if err != nil {
			return nil, err
		}
		score, err := s.GetScore(t.ID)
		if err != nil {
			return nil, err
		}
		threadViews = append(threadViews, model.ThreadView{
			Thread:   t,
			Question: q,
			Messages: msgs,
			Score:    score,
		})
	}

	grade, err := s.GetGrade(sessionID)
	if err != nil {
		return nil, err
	}

	return &model.SessionView{
		Session:   sess,
		Blueprint: bp,
		Threads:   threadViews,
		Grade:     grade,
	}, nil
}

// ListSessions returns all sessions.
func (s *Store) ListSessions() ([]model.ExamSession, error) {
	rows, err := s.db.Query(`SELECT id, blueprint_id, student_id, status, started_at, submitted_at FROM exam_sessions ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []model.ExamSession
	for rows.Next() {
		var sess model.ExamSession
		if err := rows.Scan(&sess.ID, &sess.BlueprintID, &sess.StudentID, &sess.Status, &sess.StartedAt, &sess.SubmittedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// QuestionCount returns the number of questions in the database.
func (s *Store) QuestionCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM questions`).Scan(&count)
	return count, err
}
