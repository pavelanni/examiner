package store

import (
	"database/sql"
	"fmt"
)

// StudentListItem holds the data needed to display one row in the
// student list for an exam.
type StudentListItem struct {
	SessionID   int64
	ExternalID  string
	DisplayName string
	LLMGrade    float64
	Status      string
}

// GetExamByID returns the ExamSummary for the given exam_id, or nil if
// no such exam exists.
func (s *Store) GetExamByID(examID string) (*ExamSummary, error) {
	var es ExamSummary
	err := s.db.QueryRow(`
		SELECT
			e.id,
			e.exam_id,
			e.subject,
			e.date,
			e.num_questions,
			e.imported_at,
			COUNT(DISTINCT es2.id) AS student_count,
			COUNT(DISTINCT CASE WHEN g.final_grade IS NOT NULL THEN g.session_id END) AS reviewed_count
		FROM exams e
		LEFT JOIN exam_sessions es2 ON es2.exam_db_id = e.id
		LEFT JOIN grades g ON g.session_id = es2.id
		WHERE e.exam_id = ?
		GROUP BY e.id`,
		examID).Scan(&es.ID, &es.ExamID, &es.Subject, &es.Date,
		&es.NumQuestions, &es.ImportedAt, &es.StudentCount, &es.ReviewedCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get exam by id: %w", err)
	}
	return &es, nil
}

// ListStudentsForExam returns all students for the given exam_id,
// ordered by display_name.
func (s *Store) ListStudentsForExam(examID string) ([]StudentListItem, error) {
	rows, err := s.db.Query(`
		SELECT
			es.id,
			st.external_id,
			st.display_name,
			es.llm_grade,
			es.status
		FROM exam_sessions es
		JOIN students st ON st.id = es.student_id
		JOIN exams e ON e.id = es.exam_db_id
		WHERE e.exam_id = ?
		ORDER BY st.display_name`, examID)
	if err != nil {
		return nil, fmt.Errorf("list students for exam: %w", err)
	}
	defer rows.Close()

	var items []StudentListItem
	for rows.Next() {
		var item StudentListItem
		if err := rows.Scan(&item.SessionID, &item.ExternalID, &item.DisplayName,
			&item.LLMGrade, &item.Status); err != nil {
			return nil, fmt.Errorf("scan student: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeleteExam removes an exam and all related data (students, sessions,
// questions, conversation messages, scores, grades) via ON DELETE CASCADE.
func (s *Store) DeleteExam(examID string) error {
	_, err := s.db.Exec(`DELETE FROM exams WHERE exam_id = ?`, examID)
	if err != nil {
		return fmt.Errorf("delete exam: %w", err)
	}
	return nil
}
