package store

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

// ExamSummary holds exam metadata for dashboard display.
type ExamSummary struct {
	ID            int64
	ExamID        string
	Subject       string
	Date          string
	NumQuestions  int
	StudentCount  int
	ReviewedCount int
	ImportedAt    time.Time
}

// ImportExam inserts all data from an ExamExport into the database in a single transaction.
func (s *Store) ImportExam(exp model.ExamExport) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Check for duplicate exam_id.
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM exams WHERE exam_id = ?`, exp.ExamID).Scan(&count); err != nil {
		return fmt.Errorf("check duplicate: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("exam %q already imported", exp.ExamID)
	}

	// Insert exam metadata.
	res, err := tx.Exec(`INSERT INTO exams (exam_id, subject, date, prompt_variant, num_questions, imported_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		exp.ExamID, exp.Subject, exp.Date, exp.PromptVariant, exp.NumQuestions, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert exam: %w", err)
	}
	examDBID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("exam last insert id: %w", err)
	}

	for _, sr := range exp.Results {
		if sr.Status != model.StatusGraded {
			slog.Warn("importing student with non-graded status",
				"external_id", sr.ExternalID,
				"status", sr.Status)
		}

		// Insert student.
		sRes, err := tx.Exec(`INSERT INTO students (exam_db_id, external_id, display_name) VALUES (?, ?, ?)`,
			examDBID, sr.ExternalID, sr.DisplayName)
		if err != nil {
			return fmt.Errorf("insert student %q: %w", sr.ExternalID, err)
		}
		studentID, err := sRes.LastInsertId()
		if err != nil {
			return fmt.Errorf("student last insert id: %w", err)
		}

		// Insert exam session.
		esRes, err := tx.Exec(`INSERT INTO exam_sessions (exam_db_id, student_id, session_number, status, started_at, submitted_at, llm_grade)
			VALUES (?, ?, ?, 'imported', ?, ?, ?)`,
			examDBID, studentID, sr.SessionNumber, sr.StartedAt, sr.SubmittedAt, sr.LLMGrade)
		if err != nil {
			return fmt.Errorf("insert exam_session: %w", err)
		}
		sessionID, err := esRes.LastInsertId()
		if err != nil {
			return fmt.Errorf("session last insert id: %w", err)
		}

		for pos, qr := range sr.Questions {
			// Insert question.
			qRes, err := tx.Exec(`INSERT INTO questions (session_id, position, text, topic, difficulty, max_points, rubric, model_answer)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				sessionID, pos+1, qr.Text, qr.Topic, string(qr.Difficulty), qr.MaxPoints, qr.Rubric, qr.ModelAnswer)
			if err != nil {
				return fmt.Errorf("insert question %d: %w", pos, err)
			}
			questionID, err := qRes.LastInsertId()
			if err != nil {
				return fmt.Errorf("question last insert id: %w", err)
			}

			// Insert conversation messages.
			for _, msg := range qr.Conversation {
				if _, err := tx.Exec(`INSERT INTO conversation_messages (question_id, role, content, timestamp)
					VALUES (?, ?, ?, ?)`,
					questionID, msg.Role, msg.Content, msg.At); err != nil {
					return fmt.Errorf("insert conversation msg: %w", err)
				}
			}

			// Insert score (teacher fields left as NULL defaults).
			if _, err := tx.Exec(`INSERT INTO scores (question_id, llm_score, llm_feedback) VALUES (?, ?, ?)`,
				questionID, qr.LLMScore, qr.LLMFeedback); err != nil {
				return fmt.Errorf("insert score: %w", err)
			}
		}

		// Insert grade (final_grade left as NULL).
		if _, err := tx.Exec(`INSERT INTO grades (session_id) VALUES (?)`, sessionID); err != nil {
			return fmt.Errorf("insert grade: %w", err)
		}
	}

	return tx.Commit()
}

// ListExams returns all imported exams with student and reviewed counts,
// ordered by imported_at descending.
func (s *Store) ListExams() ([]ExamSummary, error) {
	rows, err := s.db.Query(`
		SELECT
			e.id,
			e.exam_id,
			e.subject,
			e.date,
			e.num_questions,
			e.imported_at,
			COUNT(DISTINCT es.id) AS student_count,
			COUNT(DISTINCT CASE WHEN g.final_grade IS NOT NULL THEN g.session_id END) AS reviewed_count
		FROM exams e
		LEFT JOIN exam_sessions es ON es.exam_db_id = e.id
		LEFT JOIN grades g ON g.session_id = es.id
		GROUP BY e.id
		ORDER BY e.imported_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list exams: %w", err)
	}
	defer rows.Close()

	var exams []ExamSummary
	for rows.Next() {
		var es ExamSummary
		if err := rows.Scan(&es.ID, &es.ExamID, &es.Subject, &es.Date,
			&es.NumQuestions, &es.ImportedAt, &es.StudentCount, &es.ReviewedCount); err != nil {
			return nil, fmt.Errorf("scan exam: %w", err)
		}
		exams = append(exams, es)
	}
	return exams, rows.Err()
}
