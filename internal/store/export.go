package store

import (
	"fmt"

	"github.com/pavelanni/examiner/internal/model"
)

// ExportAllSessions builds export-ready student results from all sessions.
func (s *Store) ExportAllSessions() ([]model.StudentResult, error) {
	sessions, err := s.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	// Track session count per student for session_number.
	studentSessionCount := make(map[int64]int)

	var results []model.StudentResult
	for _, sess := range sessions {
		studentSessionCount[sess.StudentID]++

		view, err := s.GetSessionView(sess.ID)
		if err != nil {
			return nil, fmt.Errorf("get session %d: %w", sess.ID, err)
		}

		user, err := s.GetUserByID(sess.StudentID)
		if err != nil {
			return nil, fmt.Errorf("get user %d: %w", sess.StudentID, err)
		}

		var externalID, displayName string
		if user != nil {
			externalID = user.ExternalID
			displayName = user.DisplayName
		}

		var questions []model.QuestionResult
		for _, tv := range view.Threads {
			var conv []model.ConversationMsg
			for _, m := range tv.Messages {
				conv = append(conv, model.ConversationMsg{
					Role:    string(m.Role),
					Content: m.Content,
					At:      m.CreatedAt,
				})
			}

			qr := model.QuestionResult{
				Text:         tv.Question.Text,
				Topic:        tv.Question.Topic,
				Difficulty:   tv.Question.Difficulty,
				MaxPoints:    tv.Question.MaxPoints,
				Rubric:       tv.Question.Rubric,
				ModelAnswer:  tv.Question.ModelAnswer,
				Conversation: conv,
			}
			if tv.Score != nil {
				qr.LLMScore = tv.Score.LLMScore
				qr.LLMFeedback = tv.Score.LLMFeedback
			}
			questions = append(questions, qr)
		}

		var llmGrade float64
		if view.Grade != nil {
			llmGrade = view.Grade.LLMGrade
		}

		results = append(results, model.StudentResult{
			ExternalID:    externalID,
			DisplayName:   displayName,
			SessionNumber: studentSessionCount[sess.StudentID],
			Status:        sess.Status,
			StartedAt:     sess.StartedAt,
			SubmittedAt:   sess.SubmittedAt,
			Questions:     questions,
			LLMGrade:      llmGrade,
		})
	}

	return results, nil
}
