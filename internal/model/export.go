package model

import "time"

// ExamExport is the top-level JSON structure for exam result export.
type ExamExport struct {
	ExamID        string          `json:"exam_id"`
	Subject       string          `json:"subject"`
	Date          string          `json:"date"`
	PromptVariant string          `json:"prompt_variant"`
	NumQuestions  int             `json:"num_questions"`
	Results       []StudentResult `json:"results"`
}

// StudentResult holds one student's exam session data for export.
type StudentResult struct {
	ExternalID    string           `json:"external_id"`
	DisplayName   string           `json:"display_name"`
	SessionNumber int              `json:"session_number"`
	Status        SessionStatus    `json:"status"`
	StartedAt     time.Time        `json:"started_at"`
	SubmittedAt   *time.Time       `json:"submitted_at,omitempty"`
	Questions     []QuestionResult `json:"questions"`
	LLMGrade      float64          `json:"llm_grade"`
}

// QuestionResult holds per-question data for export.
type QuestionResult struct {
	Text         string            `json:"text"`
	Topic        string            `json:"topic"`
	Difficulty   Difficulty        `json:"difficulty"`
	MaxPoints    int               `json:"max_points"`
	Rubric       string            `json:"rubric"`
	ModelAnswer  string            `json:"model_answer"`
	Conversation []ConversationMsg `json:"conversation"`
	LLMScore     float64           `json:"llm_score"`
	LLMFeedback  string            `json:"llm_feedback"`
}

// ConversationMsg is a single message in an exported conversation.
type ConversationMsg struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}
