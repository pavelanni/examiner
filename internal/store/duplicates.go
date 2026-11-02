package store

import (
	"database/sql"
	"errors"

	"github.com/pavelanni/examiner/internal/model"
)

// FindQuestionByCourseAndText returns the question with exactly this text in
// the course, or nil if there is none.
func (s *Store) FindQuestionByCourseAndText(courseID int64, text string) (*model.Question, error) {
	q, err := scanQuestion(s.db.QueryRow(`SELECT `+questionColumns+` FROM questions WHERE course_id = ? AND text = ?`, courseID, text))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// DuplicateQuestionGroups returns the stored questions that share a topic
// and a normalized text with another question (FR1-03), grouped and ordered
// by topic, normalized text and ID. Such rows predate the duplicate checks;
// they are reported, not deleted, since sessions may reference them.
func (s *Store) DuplicateQuestionGroups() ([]model.DuplicateGroup, error) {
	rows, err := s.db.Query(`SELECT ` + qualifiedQuestionColumns + `
		FROM questions q
		JOIN (SELECT topic, text_norm FROM questions GROUP BY topic, text_norm HAVING COUNT(*) > 1) d
		  ON d.topic = q.topic AND d.text_norm = q.text_norm
		ORDER BY q.topic, q.text_norm, q.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var groups []model.DuplicateGroup
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		if n := len(groups); n == 0 || groups[n-1].Topic != q.Topic || groups[n-1].Questions[0].TextNorm != q.TextNorm {
			groups = append(groups, model.DuplicateGroup{Topic: q.Topic})
		}
		g := &groups[len(groups)-1]
		g.Questions = append(g.Questions, q)
	}
	return groups, rows.Err()
}
