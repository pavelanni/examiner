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

// ExamInfo holds exam metadata stored in the database.
type ExamInfo struct {
	ExamID        string
	Subject       string
	Date          string
	PromptVariant string
	NumQuestions  int
}

// ExamManifest describes an exam preparation manifest read from YAML.
type ExamManifest struct {
	ExamID        string `yaml:"exam_id"`
	Subject       string `yaml:"subject"`
	Date          string `yaml:"date"`
	Lang          string `yaml:"lang"`
	PromptVariant string `yaml:"prompt_variant"`
	NumQuestions  int    `yaml:"num_questions"`
	MaxFollowups  int    `yaml:"max_followups"`
	Shuffle       bool   `yaml:"shuffle"`
	Questions     string `yaml:"questions"`
	Roster        string `yaml:"roster"`
}

// ConversationMsg is a single message in an exported conversation.
type ConversationMsg struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}
