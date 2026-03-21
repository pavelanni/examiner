package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Store wraps the grader SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the grader database and runs migrations.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL DEFAULT '',
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'teacher',
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL
		);

		CREATE TABLE IF NOT EXISTS auth_sessions (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id),
			created_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires
			ON auth_sessions(expires_at);

		CREATE TABLE IF NOT EXISTS exams (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exam_id TEXT NOT NULL UNIQUE,
			subject TEXT NOT NULL DEFAULT '',
			date TEXT NOT NULL DEFAULT '',
			prompt_variant TEXT NOT NULL DEFAULT '',
			num_questions INTEGER NOT NULL DEFAULT 0,
			imported_at DATETIME NOT NULL
		);

		CREATE TABLE IF NOT EXISTS students (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exam_db_id INTEGER NOT NULL REFERENCES exams(id) ON DELETE CASCADE,
			external_id TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS exam_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exam_db_id INTEGER NOT NULL REFERENCES exams(id) ON DELETE CASCADE,
			student_id INTEGER NOT NULL REFERENCES students(id) ON DELETE CASCADE,
			session_number INTEGER NOT NULL DEFAULT 1,
			status TEXT NOT NULL DEFAULT 'imported',
			started_at DATETIME,
			submitted_at DATETIME,
			llm_grade REAL NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS questions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id INTEGER NOT NULL REFERENCES exam_sessions(id) ON DELETE CASCADE,
			position INTEGER NOT NULL DEFAULT 0,
			text TEXT NOT NULL,
			topic TEXT NOT NULL DEFAULT '',
			difficulty TEXT NOT NULL DEFAULT '',
			max_points INTEGER NOT NULL DEFAULT 10,
			rubric TEXT NOT NULL DEFAULT '',
			model_answer TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS conversation_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			timestamp DATETIME
		);

		CREATE TABLE IF NOT EXISTS scores (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			question_id INTEGER NOT NULL UNIQUE REFERENCES questions(id) ON DELETE CASCADE,
			llm_score REAL NOT NULL DEFAULT 0,
			llm_feedback TEXT NOT NULL DEFAULT '',
			teacher_score REAL,
			teacher_comment TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS grades (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id INTEGER NOT NULL UNIQUE REFERENCES exam_sessions(id) ON DELETE CASCADE,
			final_grade REAL,
			teacher_comment TEXT NOT NULL DEFAULT '',
			reviewed_by INTEGER,
			reviewed_at DATETIME
		);
	`)
	return err
}
