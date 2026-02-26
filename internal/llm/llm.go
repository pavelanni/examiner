package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"unicode/utf8"

	"github.com/pavelanni/examiner/internal/llm/prompts"
	"github.com/pavelanni/examiner/internal/model"

	openai "github.com/sashabaranov/go-openai"
)

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
}

// New creates a new LLM client.
func New(baseURL, apiKey, modelName string, variant string) (*Client, error) {
	v := prompts.PromptVariant(variant)
	if !prompts.IsValidVariant(string(v)) {
		v = prompts.PromptStandard
		slog.Warn("invalid prompt variant, using standard", "variant", variant)
	}

	if err := prompts.Load(promptsFS); err != nil {
		return nil, fmt.Errorf("failed to load prompts: %w", err)
	}

	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	return &Client{
		api:           openai.NewClientWithConfig(config),
		model:         modelName,
		promptVariant: v,
	}, nil
}

// Ping checks that the LLM endpoint is reachable by listing available models.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.api.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("LLM endpoint unreachable: %w", err)
	}
	return nil
}

// EvaluateAnswer sends the student's answer (and any prior conversation) to the LLM
// for evaluation. It returns the LLM's response which may include a follow-up question.
func (c *Client) EvaluateAnswer(ctx context.Context, question model.Question, messages []model.Message, maxFollowups int, sessionID, threadID int64) (*GradeResult, string, error) {
	systemPrompt, err := prompts.BuildEvalPrompt(c.promptVariant, question, messages, maxFollowups)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build eval prompt: %w", err)
	}

	chatMsgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
	}

	for _, m := range messages {
		role := openai.ChatMessageRoleUser
		if m.Role == model.RoleLLM {
			role = openai.ChatMessageRoleAssistant
		}
		chatMsgs = append(chatMsgs, openai.ChatCompletionMessage{
			Role:    role,
			Content: m.Content,
		})
	}

	resp, err := c.api.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: chatMsgs,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
		Temperature: 0.3,
	})
	if err != nil {
		return nil, "", fmt.Errorf("LLM API call: %w", err)
	}

	slog.Info("LLM token usage",
		"op", "evaluate",
		"model", c.model,
		"session_id", sessionID,
		"thread_id", threadID,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"total_tokens", resp.Usage.TotalTokens,
	)

	if len(resp.Choices) == 0 {
		return nil, "", fmt.Errorf("LLM returned no choices")
	}

	raw := resp.Choices[0].Message.Content
	slog.Debug("LLM response", "raw", raw)

	var result GradeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, raw, fmt.Errorf("parse LLM response: %w (raw: %s)", err, raw)
	}

	validateGradeResult(&result, question.MaxPoints)

	return &result, raw, nil
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

	for _, m := range messages {
		role := openai.ChatMessageRoleUser
		if m.Role == model.RoleLLM {
			role = openai.ChatMessageRoleAssistant
		}
		chatMsgs = append(chatMsgs, openai.ChatCompletionMessage{
			Role:    role,
			Content: m.Content,
		})
	}

	resp, err := c.api.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: chatMsgs,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
		Temperature: 0.1,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM grading API call: %w", err)
	}

	slog.Info("LLM token usage",
		"op", "grade",
		"model", c.model,
		"session_id", sessionID,
		"thread_id", threadID,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"total_tokens", resp.Usage.TotalTokens,
	)

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned no choices for grading")
	}

	raw := resp.Choices[0].Message.Content
	var result GradeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parse grading response: %w (raw: %s)", err, raw)
	}

	validateGradeResult(&result, question.MaxPoints)

	return &result, nil
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
