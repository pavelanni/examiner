package store

import (
	"database/sql"
	"fmt"
	"time"
)

// ReviewData holds all data needed for the review page.
type ReviewData struct {
	ExamID      string
	Subject     string
	Date        string
	SessionID   int64
	StudentName string
	ExternalID  string
	LLMGrade    float64
	Status      string
	Questions   []ReviewQuestion
	Grade       *GradeInfo
}

// ReviewQuestion holds per-question data for the review page.
type ReviewQuestion struct {
	ID             int64
	Position       int
	Text           string
	Topic          string
	Difficulty     string
	MaxPoints      int
	Rubric         string
	ModelAnswer    string
	Messages       []ConversationMessage
	LLMScore       float64
	LLMFeedback    string
	TeacherScore   *float64
	TeacherComment string
}

// ConversationMessage holds a single message in a conversation.
type ConversationMessage struct {
	Role      string
	Content   string
	Timestamp time.Time
}

// GradeInfo holds grade data for a session.
type GradeInfo struct {
	FinalGrade     *float64
	TeacherComment string
	ReviewedBy     *int64
	ReviewedAt     *time.Time
}

// GetReviewData builds the full review page data for a given session.
func (s *Store) GetReviewData(sessionID int64) (*ReviewData, error) {
	// Query session info with student and exam metadata.
	var rd ReviewData
	rd.SessionID = sessionID
	err := s.db.QueryRow(`
		SELECT
			e.exam_id,
			e.subject,
			e.date,
			st.display_name,
			st.external_id,
			es.llm_grade,
			es.status
		FROM exam_sessions es
		JOIN students st ON st.id = es.student_id
		JOIN exams e ON e.id = es.exam_db_id
		WHERE es.id = ?`, sessionID).Scan(
		&rd.ExamID, &rd.Subject, &rd.Date,
		&rd.StudentName, &rd.ExternalID,
		&rd.LLMGrade, &rd.Status,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session info: %w", err)
	}

	// Query questions for this session.
	qRows, err := s.db.Query(`
		SELECT
			q.id,
			q.position,
			q.text,
			q.topic,
			q.difficulty,
			q.max_points,
			q.rubric,
			q.model_answer,
			sc.llm_score,
			sc.llm_feedback,
			sc.teacher_score,
			sc.teacher_comment
		FROM questions q
		LEFT JOIN scores sc ON sc.question_id = q.id
		WHERE q.session_id = ?
		ORDER BY q.position`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query questions: %w", err)
	}
	defer qRows.Close()

	for qRows.Next() {
		var rq ReviewQuestion
		if err := qRows.Scan(
			&rq.ID, &rq.Position, &rq.Text, &rq.Topic,
			&rq.Difficulty, &rq.MaxPoints, &rq.Rubric, &rq.ModelAnswer,
			&rq.LLMScore, &rq.LLMFeedback,
			&rq.TeacherScore, &rq.TeacherComment,
		); err != nil {
			return nil, fmt.Errorf("scan question: %w", err)
		}
		rd.Questions = append(rd.Questions, rq)
	}
	if err := qRows.Err(); err != nil {
		return nil, fmt.Errorf("questions rows: %w", err)
	}

	// Query conversation messages for each question.
	for i := range rd.Questions {
		mRows, err := s.db.Query(`
			SELECT role, content, timestamp
			FROM conversation_messages
			WHERE question_id = ?
			ORDER BY id`, rd.Questions[i].ID)
		if err != nil {
			return nil, fmt.Errorf("query messages for question %d: %w", rd.Questions[i].ID, err)
		}

		for mRows.Next() {
			var cm ConversationMessage
			var ts *time.Time
			if err := mRows.Scan(&cm.Role, &cm.Content, &ts); err != nil {
				mRows.Close()
				return nil, fmt.Errorf("scan message: %w", err)
			}
			if ts != nil {
				cm.Timestamp = *ts
			}
			rd.Questions[i].Messages = append(rd.Questions[i].Messages, cm)
		}
		mRows.Close()
		if err := mRows.Err(); err != nil {
			return nil, fmt.Errorf("messages rows: %w", err)
		}
	}

	// Query grade info.
	var gi GradeInfo
	err = s.db.QueryRow(`
		SELECT final_grade, teacher_comment, reviewed_by, reviewed_at
		FROM grades
		WHERE session_id = ?`, sessionID).Scan(
		&gi.FinalGrade, &gi.TeacherComment, &gi.ReviewedBy, &gi.ReviewedAt,
	)
	if err == sql.ErrNoRows {
		// No grade row; leave Grade as nil.
	} else if err != nil {
		return nil, fmt.Errorf("get grade: %w", err)
	} else {
		rd.Grade = &gi
	}

	return &rd, nil
}

// UpdateTeacherScore sets the teacher score and comment for a question.
func (s *Store) UpdateTeacherScore(questionID int64, score float64, comment string) error {
	_, err := s.db.Exec(
		`UPDATE scores SET teacher_score = ?, teacher_comment = ? WHERE question_id = ?`,
		score, comment, questionID,
	)
	if err != nil {
		return fmt.Errorf("update teacher score: %w", err)
	}
	return nil
}

// FinalizeGrade sets the final grade, marks the session as reviewed,
// and records who reviewed it.
func (s *Store) FinalizeGrade(sessionID int64, finalGrade float64, comment string, reviewerID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(
		`UPDATE grades SET final_grade = ?, teacher_comment = ?, reviewed_by = ?, reviewed_at = ? WHERE session_id = ?`,
		finalGrade, comment, reviewerID, time.Now().UTC(), sessionID,
	); err != nil {
		return fmt.Errorf("update grade: %w", err)
	}

	if _, err := tx.Exec(
		`UPDATE exam_sessions SET status = 'reviewed' WHERE id = ?`,
		sessionID,
	); err != nil {
		return fmt.Errorf("update session status: %w", err)
	}

	return tx.Commit()
}

// SetSessionStatus updates the status of an exam session.
func (s *Store) SetSessionStatus(sessionID int64, status string) error {
	_, err := s.db.Exec(
		`UPDATE exam_sessions SET status = ? WHERE id = ?`,
		status, sessionID,
	)
	if err != nil {
		return fmt.Errorf("set session status: %w", err)
	}
	return nil
}
