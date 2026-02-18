package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pavelanni/examiner/internal/model"

	openai "github.com/sashabaranov/go-openai"
)

// GradeResult holds the LLM's assessment of a single answer thread.
type GradeResult struct {
	Score       float64 `json:"score"`
	MaxPoints   int     `json:"max_points"`
	Feedback    string  `json:"feedback"`
	NeedFollowup bool   `json:"need_followup"`
	FollowupQ   string  `json:"followup_question"`
}

// Client wraps an OpenAI-compatible API client.
type Client struct {
	api   *openai.Client
	model string
}

// New creates a new LLM client.
func New(baseURL, apiKey, modelName string) *Client {
	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	return &Client{
		api:   openai.NewClientWithConfig(config),
		model: modelName,
	}
}

// EvaluateAnswer sends the student's answer (and any prior conversation) to the LLM
// for evaluation. It returns the LLM's response which may include a follow-up question.
func (c *Client) EvaluateAnswer(ctx context.Context, question model.Question, messages []model.Message, maxFollowups int) (*GradeResult, string, error) {
	followupsUsed := countFollowups(messages)
	canFollowup := followupsUsed < maxFollowups

	systemPrompt := buildEvalSystemPrompt(question, canFollowup)

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

	if len(resp.Choices) == 0 {
		return nil, "", fmt.Errorf("LLM returned no choices")
	}

	raw := resp.Choices[0].Message.Content
	slog.Debug("LLM response", "raw", raw)

	var result GradeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, raw, fmt.Errorf("parse LLM response: %w (raw: %s)", err, raw)
	}

	return &result, raw, nil
}

// GradeThread produces a final score for an entire question thread.
func (c *Client) GradeThread(ctx context.Context, question model.Question, messages []model.Message) (*GradeResult, error) {
	systemPrompt := buildGradingSystemPrompt(question)

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

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned no choices for grading")
	}

	raw := resp.Choices[0].Message.Content
	var result GradeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parse grading response: %w (raw: %s)", err, raw)
	}

	result.MaxPoints = question.MaxPoints
	return &result, nil
}

func buildEvalSystemPrompt(q model.Question, canFollowup bool) string {
	var sb strings.Builder
	sb.WriteString("You are an exam evaluator. A student is answering the following question:\n\n")
	sb.WriteString("QUESTION: " + q.Text + "\n\n")
	sb.WriteString(fmt.Sprintf("MAX POINTS: %d\n\n", q.MaxPoints))

	if q.Rubric != "" {
		sb.WriteString("GRADING RUBRIC:\n" + q.Rubric + "\n\n")
	}
	if q.ModelAnswer != "" {
		sb.WriteString("MODEL ANSWER (not shown to student):\n" + q.ModelAnswer + "\n\n")
	}

	sb.WriteString("INSTRUCTIONS:\n")
	sb.WriteString("- Evaluate the student's answer for correctness, completeness, and understanding.\n")
	if canFollowup {
		sb.WriteString("- If the answer is incomplete, vague, or partially correct, you MAY ask ONE follow-up question to probe deeper understanding.\n")
		sb.WriteString("- Only ask a follow-up if it would meaningfully help assess the student's knowledge.\n")
		sb.WriteString("- If the answer is clearly correct and complete, or clearly wrong with no ambiguity, do NOT ask a follow-up.\n")
	} else {
		sb.WriteString("- Maximum follow-up questions reached. Do NOT ask any more follow-ups. Set need_followup to false.\n")
	}
	sb.WriteString("\nRespond ONLY with a JSON object with these fields:\n")
	sb.WriteString(`{"score": <number 0 to max_points>, "max_points": <max_points>, "feedback": "<brief feedback>", "need_followup": <true/false>, "followup_question": "<question or empty string>"}`)
	sb.WriteString("\n")

	return sb.String()
}

func buildGradingSystemPrompt(q model.Question) string {
	var sb strings.Builder
	sb.WriteString("You are a final exam grader. Review the entire conversation thread below ")
	sb.WriteString("and produce a FINAL score for the student's performance on this question.\n\n")
	sb.WriteString("QUESTION: " + q.Text + "\n\n")
	sb.WriteString(fmt.Sprintf("MAX POINTS: %d\n\n", q.MaxPoints))

	if q.Rubric != "" {
		sb.WriteString("GRADING RUBRIC:\n" + q.Rubric + "\n\n")
	}
	if q.ModelAnswer != "" {
		sb.WriteString("MODEL ANSWER:\n" + q.ModelAnswer + "\n\n")
	}

	sb.WriteString("Consider the initial answer AND all follow-up responses.\n")
	sb.WriteString("Provide a comprehensive final assessment.\n\n")
	sb.WriteString("Respond ONLY with a JSON object:\n")
	sb.WriteString(`{"score": <number 0 to max_points>, "max_points": <max_points>, "feedback": "<comprehensive feedback>", "need_followup": false, "followup_question": ""}`)
	sb.WriteString("\n")

	return sb.String()
}

func countFollowups(messages []model.Message) int {
	count := 0
	for _, m := range messages {
		if m.Role == model.RoleLLM {
			count++
		}
	}
	return count
}
