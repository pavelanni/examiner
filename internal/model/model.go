package model

import (
	"context"
	"time"
)

// UserRole represents a user's access level (distinct from Role which is chat message roles).
type UserRole string

const (
	UserRoleStudent UserRole = "student"
	UserRoleTeacher UserRole = "teacher"
	UserRoleAdmin   UserRole = "admin"
)

type User struct {
	ID           int64
	Username     string
	DisplayName  string
	PasswordHash string
	Role         UserRole
	Active       bool
	CreatedAt    time.Time
}

type AuthSession struct {
	ID        string
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

type userCtxKey struct{}

// ContextWithUser stores a user in the request context.
func ContextWithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userCtxKey{}, u)
}

// UserFromContext retrieves the authenticated user from context, or nil.
func UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(userCtxKey{}).(*User)
	return u
}

type basePathCtxKey struct{}

// ContextWithBasePath stores the base path prefix in context.
func ContextWithBasePath(ctx context.Context, basePath string) context.Context {
	return context.WithValue(ctx, basePathCtxKey{}, basePath)
}

// BasePathFromContext retrieves the base path from context (empty string if not set).
func BasePathFromContext(ctx context.Context) string {
	bp, _ := ctx.Value(basePathCtxKey{}).(string)
	return bp
}

type csrfCtxKey struct{}

// ContextWithCSRFToken stores the CSRF token in context.
func ContextWithCSRFToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfCtxKey{}, token)
}

// CSRFTokenFromContext retrieves the CSRF token from context.
func CSRFTokenFromContext(ctx context.Context) string {
	t, _ := ctx.Value(csrfCtxKey{}).(string)
	return t
}

type Role string

const (
	RoleStudent Role = "student"
	RoleTeacher Role = "teacher"
	RoleSystem  Role = "system"
	RoleLLM     Role = "assistant"
)

type SessionStatus string

const (
	StatusInProgress SessionStatus = "in_progress"
	StatusSubmitted  SessionStatus = "submitted"
	StatusGrading    SessionStatus = "grading"
	StatusGraded     SessionStatus = "graded"
	StatusReviewed   SessionStatus = "reviewed"
)

type ThreadStatus string

const (
	ThreadOpen      ThreadStatus = "open"
	ThreadAnswered  ThreadStatus = "answered"
	ThreadCompleted ThreadStatus = "completed"
)

type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
)

type Question struct {
	ID          int64      `json:"id"`
	CourseID    int64      `json:"course_id"`
	Text        string     `json:"text"`
	Difficulty  Difficulty `json:"difficulty"`
	Topic       string     `json:"topic"`
	Rubric      string     `json:"rubric"`
	ModelAnswer string     `json:"model_answer"`
	MaxPoints   int        `json:"max_points"`
}

type ExamBlueprint struct {
	ID           int64  `json:"id"`
	CourseID     int64  `json:"course_id"`
	Name         string `json:"name"`
	TimeLimit    int    `json:"time_limit"`
	MaxFollowups int    `json:"max_followups"`
}

type ExamSession struct {
	ID          int64         `json:"id"`
	BlueprintID int64         `json:"blueprint_id"`
	StudentID   int64         `json:"student_id"`
	Status      SessionStatus `json:"status"`
	StartedAt   time.Time     `json:"started_at"`
	SubmittedAt *time.Time    `json:"submitted_at,omitempty"`
}

type QuestionThread struct {
	ID         int64        `json:"id"`
	SessionID  int64        `json:"session_id"`
	QuestionID int64        `json:"question_id"`
	Status     ThreadStatus `json:"status"`
}

type Message struct {
	ID         int64     `json:"id"`
	ThreadID   int64     `json:"thread_id"`
	Role       Role      `json:"role"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
	TokenCount int       `json:"token_count"`
}

type QuestionScore struct {
	ID             int64    `json:"id"`
	ThreadID       int64    `json:"thread_id"`
	LLMScore       float64  `json:"llm_score"`
	LLMFeedback    string   `json:"llm_feedback"`
	TeacherScore   *float64 `json:"teacher_score,omitempty"`
	TeacherComment string   `json:"teacher_comment,omitempty"`
}

type Grade struct {
	ID         int64      `json:"id"`
	SessionID  int64      `json:"session_id"`
	LLMGrade   float64    `json:"llm_grade"`
	FinalGrade *float64   `json:"final_grade,omitempty"`
	ReviewedBy *int64     `json:"reviewed_by,omitempty"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

// ExamConfig holds runtime exam parameters set via CLI flags.
type ExamConfig struct {
	NumQuestions  int    // 0 means all available
	Difficulty    string // empty means all difficulties
	Topic         string // empty means all topics
	MaxFollowups  int
	Shuffle       bool
	BasePath      string // URL prefix for sub-path deployments (e.g. "/ru")
	SecureCookies bool   // Set Secure flag on cookies (disable for local dev)
	PromptVariant string // Grading prompt variant (strict, standard, lenient)
}

// QuestionImport is used for loading questions from JSON.
type QuestionImport struct {
	Text        string     `json:"text"`
	Difficulty  Difficulty `json:"difficulty"`
	Topic       string     `json:"topic"`
	Rubric      string     `json:"rubric"`
	ModelAnswer string     `json:"model_answer"`
	MaxPoints   int        `json:"max_points"`
}

// ThreadView combines thread data with question and messages for display.
type ThreadView struct {
	Thread   QuestionThread
	Question Question
	Messages []Message
	Score    *QuestionScore
}

// SessionView combines session data with threads for display.
type SessionView struct {
	Session   ExamSession
	Blueprint ExamBlueprint
	Threads   []ThreadView
	Grade     *Grade
}
