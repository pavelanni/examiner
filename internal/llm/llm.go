package llm

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/pavelanni/examiner/internal/llm/prompts"
	"github.com/pavelanni/examiner/internal/model"

	openai "github.com/sashabaranov/go-openai"
)

// dialogueTrigger is the only user message sent with a prompt. The dialogue
// itself is part of the system prompt; sending it again as chat history
// would show the model every answer twice.
const dialogueTrigger = "Evaluate the dialogue in the instructions above and respond with the JSON object only."

const (
	maxFeedbackLen = 5000
	maxFollowupLen = 5000
)

// GradeResult holds the LLM's assessment of a single answer thread.
type GradeResult struct {
	Score        float64 `json:"score"`
	MaxPoints    int     `json:"max_points"`
	Feedback     string  `json:"feedback"`
	NeedFollowup bool    `json:"need_followup"`
	FollowupQ    string  `json:"followup_question"`
}

// Client wraps an OpenAI-compatible API client.
type Client struct {
	api           *openai.Client
	model         string
	promptVariant prompts.PromptVariant
	settings      settings
	slots         chan struct{}
	gradingSlots  chan struct{}
}

// New creates a new LLM client.
func New(baseURL, apiKey, modelName string, variant string, options ...Option) (*Client, error) {
	settings := defaultSettings()
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("nil LLM option")
		}
		option(&settings)
	}
	if err := settings.validate(); err != nil {
		return nil, err
	}
	v := prompts.PromptVariant(variant)
	if !prompts.IsValidVariant(string(v)) {
		v = prompts.PromptStandard
		slog.Warn("invalid prompt variant, using standard", "variant", variant)
	}

	if err := prompts.Load(promptsFS); err != nil {
		return nil, fmt.Errorf("failed to load prompts: %w", err)
	}

	config := openai.DefaultConfig(apiKey)
	config.HTTPClient = &http.Client{Timeout: settings.timeout}
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	gradingLimit := max(1, settings.maxConcurrency-1)
	return &Client{
		settings:      settings,
		slots:         make(chan struct{}, settings.maxConcurrency),
		gradingSlots:  make(chan struct{}, gradingLimit),
		api:           openai.NewClientWithConfig(config),
		model:         modelName,
		promptVariant: v,
	}, nil
}

// Ping checks that the LLM endpoint is reachable by listing available models.
func (c *Client) Ping(ctx context.Context) (err error) {
	started := time.Now()
	defer func() {
		slog.Info("LLM request completed", "op", "ping", "model", c.model, "attempt", 1,
			"duration", time.Since(started), "result", outcome(err), "status", statusCode(err))
	}()
	ctx, cancel := context.WithTimeout(ctx, c.settings.timeout)
	defer cancel()
	if err := acquire(ctx, c.slots); err != nil {
		return err
	}
	defer func() { <-c.slots }()
	_, err = c.api.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("LLM endpoint unreachable: %w", err)
	}
	return nil
}

// appendTrigger adds the single user message that follows the system prompt.
func appendTrigger(msgs []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	return append(msgs, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: dialogueTrigger,
	})
}

// EvaluateAnswer sends the dialogue so far (all student answers and the
// follow-up questions, built into the prompt) to the LLM for evaluation.
// It returns the LLM's response which may include a follow-up question.
func (c *Client) EvaluateAnswer(ctx context.Context, question model.Question, messages []model.Message, maxFollowups int, sessionID, threadID int64) (*GradeResult, string, error) {
	systemPrompt, err := prompts.BuildEvalPrompt(c.promptVariant, question, messages, maxFollowups)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build eval prompt: %w", err)
	}

	chatMsgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
	}

	chatMsgs = appendTrigger(chatMsgs)

	result, raw, err := c.complete(ctx, openai.ChatCompletionRequest{
		Model:          c.model,
		Messages:       chatMsgs,
		ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
		Temperature:    0.3,
	}, "evaluate", sessionID, threadID)
	if err != nil {
		return nil, raw, err
	}

	validateGradeResult(result, question.MaxPoints)

	return result, raw, nil
}

// GradeThread produces a final score for an entire question thread.
func (c *Client) GradeThread(ctx context.Context, question model.Question, messages []model.Message, sessionID, threadID int64) (*GradeResult, error) {
	systemPrompt, err := prompts.BuildGradePrompt(c.promptVariant, question, messages)
	if err != nil {
		return nil, fmt.Errorf("failed to build grade prompt: %w", err)
	}

	chatMsgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
	}

	chatMsgs = appendTrigger(chatMsgs)

	result, _, err := c.complete(ctx, openai.ChatCompletionRequest{
		Model:          c.model,
		Messages:       chatMsgs,
		ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
		Temperature:    0.1,
	}, "grade", sessionID, threadID)
	if err != nil {
		return nil, err
	}

	validateGradeResult(result, question.MaxPoints)

	return result, nil
}

func validateGradeResult(result *GradeResult, maxPoints int) {
	originalScore := result.Score
	result.Score = math.Max(0, math.Min(float64(maxPoints), result.Score))
	if result.Score != originalScore {
		var msg string
		if result.Score == 0 {
			msg = "LLM score clamped to lower bound (0) - possible prompt injection"
		} else if result.Score == float64(maxPoints) {
			msg = "LLM score clamped to upper bound (maxPoints) - possible prompt injection"
		} else {
			msg = "LLM score clamped - possible prompt injection"
		}
		slog.Warn(msg,
			"original_score", originalScore,
			"max_points", maxPoints,
			"clamped_score", result.Score,
		)
	}

	if result.MaxPoints != maxPoints {
		slog.Warn("LLM returned mismatched MaxPoints - overriding",
			"llm_max_points", result.MaxPoints,
			"actual_max_points", maxPoints,
		)
		result.MaxPoints = maxPoints
	}

	if utf8.RuneCountInString(result.Feedback) > maxFeedbackLen {
		runes := []rune(result.Feedback)
		result.Feedback = string(runes[:maxFeedbackLen])
		slog.Warn("LLM feedback truncated", "max_len", maxFeedbackLen)
	}

	if utf8.RuneCountInString(result.FollowupQ) > maxFollowupLen {
		runes := []rune(result.FollowupQ)
		result.FollowupQ = string(runes[:maxFollowupLen])
		slog.Warn("LLM followup question truncated", "max_len", maxFollowupLen)
	}
}
