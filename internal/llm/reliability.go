package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

const (
	DefaultTimeout        = 90 * time.Second
	DefaultMaxRetries     = 3
	DefaultMaxConcurrency = 4
	maxBackoff            = 10 * time.Second
)

// settings хранят неизменяемые параметры защиты вызовов LLM.
type settings struct {
	timeout        time.Duration
	maxRetries     int
	maxConcurrency int
}

// Option задаёт параметр клиента; значения проверяются при вызове New.
type Option func(*settings)

// WithTimeout ограничивает одну попытку, включая ожидание свободного слота.
func WithTimeout(timeout time.Duration) Option {
	return func(s *settings) { s.timeout = timeout }
}

// WithMaxRetries задаёт число повторов после первоначальной попытки (0–100).
func WithMaxRetries(retries int) Option {
	return func(s *settings) { s.maxRetries = retries }
}

// WithMaxConcurrency ограничивает общее число выполняемых запросов к LLM.
func WithMaxConcurrency(limit int) Option {
	return func(s *settings) { s.maxConcurrency = limit }
}

func defaultSettings() settings {
	return settings{timeout: DefaultTimeout, maxRetries: DefaultMaxRetries, maxConcurrency: DefaultMaxConcurrency}
}

// validate отклоняет значения, отключающие защиту или переполняющие общий бюджет.
func (s settings) validate() error {
	if s.timeout <= 0 {
		return fmt.Errorf("llm-timeout must be positive")
	}
	if s.maxRetries < 0 || s.maxRetries > 100 {
		return fmt.Errorf("llm-max-retries must be between 0 and 100")
	}
	if s.maxConcurrency <= 0 {
		return fmt.Errorf("llm-max-concurrency must be positive")
	}
	retryBudget := time.Duration(s.maxRetries) * maxBackoff
	if s.timeout > (time.Duration(1<<63-1)-retryBudget)/time.Duration(s.maxRetries+1) {
		return fmt.Errorf("llm-timeout is too large for the retry budget")
	}
	return nil
}

// ErrQueueTimeout означает, что запрос не получил слот и не был отправлен модели.
var ErrQueueTimeout = errors.New("LLM capacity wait timed out")

// queueError отличает нехватку ёмкости от тайм-аута уже отправленного запроса.
func queueError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrQueueTimeout, err)
	}
	return err
}

var errInvalidResponse = errors.New("invalid LLM response")

// acquire ожидает разрешение, не переживая отмену или дедлайн попытки.
func acquire(ctx context.Context, slots chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return queueError(err)
	}
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return queueError(ctx.Err())
	}
}

// complete повторяет только временные ошибки, освобождая слоты на время задержки.
// Общий бюджет ограничивает также очередь и чрезмерно большой Retry-After.
func (c *Client) complete(ctx context.Context, request openai.ChatCompletionRequest, op string, sessionID, threadID int64) (*GradeResult, string, error) {
	budget := c.settings.timeout*time.Duration(c.settings.maxRetries+1) + maxBackoff*time.Duration(c.settings.maxRetries)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var raw string
	for attempt := 0; ; attempt++ {
		started := time.Now()
		attemptCtx, attemptCancel := context.WithTimeout(ctx, c.settings.timeout)
		result, content, headers, err := c.attempt(attemptCtx, request, op, sessionID, threadID)
		attemptCancel()
		raw = content
		slog.Info("LLM request completed", "op", op, "model", c.model, "session_id", sessionID, "thread_id", threadID,
			"attempt", attempt+1, "duration", time.Since(started), "result", outcome(err), "status", statusCode(err))
		if err == nil {
			return result, raw, nil
		}
		if errors.Is(err, ErrQueueTimeout) {
			return nil, raw, fmt.Errorf("LLM %s: %w", op, err)
		}
		if ctx.Err() != nil {
			return nil, raw, fmt.Errorf("LLM %s: %w", op, ctx.Err())
		}
		if attempt >= c.settings.maxRetries || !retryable(err) {
			return nil, raw, fmt.Errorf("LLM %s: %w", op, err)
		}
		delay := retryDelay(attempt)
		if after := retryAfter(headers.Get("Retry-After"), time.Now()); after > delay {
			delay = after
		}
		slog.Info("LLM retry scheduled", "op", op, "session_id", sessionID, "thread_id", threadID, "attempt", attempt+2, "delay", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, raw, fmt.Errorf("LLM %s retry: %w", op, ctx.Err())
		case <-timer.C:
		}
	}
}

// attempt выполняет один запрос. Фоновая оценка занимает максимум N-1 слотов,
// оставляя один интерактивным ответам при общем лимите N > 1.
func (c *Client) attempt(ctx context.Context, request openai.ChatCompletionRequest, op string, sessionID, threadID int64) (*GradeResult, string, http.Header, error) {
	if op == "grade" {
		if err := acquire(ctx, c.gradingSlots); err != nil {
			return nil, "", nil, err
		}
		defer func() { <-c.gradingSlots }()
	}
	if err := acquire(ctx, c.slots); err != nil {
		return nil, "", nil, err
	}
	defer func() { <-c.slots }()
	resp, err := c.api.CreateChatCompletion(ctx, request)
	if err != nil {
		return nil, "", resp.Header(), err
	}
	slog.Info("LLM token usage", "op", op, "model", c.model, "session_id", sessionID, "thread_id", threadID,
		"prompt_tokens", resp.Usage.PromptTokens, "completion_tokens", resp.Usage.CompletionTokens, "total_tokens", resp.Usage.TotalTokens)
	if len(resp.Choices) == 0 {
		return nil, "", resp.Header(), fmt.Errorf("%w: no choices", errInvalidResponse)
	}
	raw := resp.Choices[0].Message.Content
	var result *GradeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, raw, resp.Header(), fmt.Errorf("%w: %v", errInvalidResponse, err)
	}
	if result == nil {
		return nil, raw, resp.Header(), fmt.Errorf("%w: null grade", errInvalidResponse)
	}
	return result, raw, resp.Header(), nil
}

// statusCode извлекает HTTP-статус даже для ответа с нестандартным телом ошибки.
func statusCode(err error) int {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode
	}
	var requestErr *openai.RequestError
	if errors.As(err, &requestErr) {
		return requestErr.HTTPStatusCode
	}
	return 0
}

// retryable не повторяет постоянные HTTP-ошибки и отменённые запросы.
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrQueueTimeout) {
		return false
	}
	if status := statusCode(err); status != 0 {
		return status == http.StatusTooManyRequests || status >= 500 && status <= 599
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errInvalidResponse) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var netErr net.Error
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.As(err, &netErr)
}

// retryDelay использует ограниченную экспоненциальную задержку с jitter.
func retryDelay(attempt int) time.Duration {
	delay := 250 * time.Millisecond
	for i := 0; i < attempt && delay < maxBackoff; i++ {
		delay = min(delay*2, maxBackoff)
	}
	return delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
}

// retryAfter понимает секунды и HTTP-date; слишком большое число насыщается.
func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value != "" && strings.Trim(value, "0123456789") == "" {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64((1<<63-1)/time.Second) {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, date.Sub(now))
	}
	return 0
}

// outcome даёт короткий результат для журналов без текста запросов и ответов.
func outcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrQueueTimeout):
		return "queue_timeout"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case statusCode(err) != 0:
		return "http_error"
	case errors.Is(err, errInvalidResponse):
		return "invalid_response"
	default:
		return "error"
	}
}
