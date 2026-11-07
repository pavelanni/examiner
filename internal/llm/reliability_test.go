package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/model"
)

const validGrade = `{"choices":[{"message":{"role":"assistant","content":"{\"score\":5,\"max_points\":10,\"feedback\":\"ok\"}"}}]}`

// reliabilityClient создаёт настоящий HTTP-клиент для изолированного LLM-сервера.
func reliabilityClient(t *testing.T, handler http.HandlerFunc, options ...Option) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, "test-key", "test-model", "standard", options...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// callGrade упражняет оба публичных способа оценки одним сценарием.
func callGrade(ctx context.Context, client *Client, grading bool) error {
	q := model.Question{Text: "Question", MaxPoints: 10}
	messages := []model.Message{{Role: model.RoleStudent, Content: "Answer"}}
	var result *GradeResult
	var err error
	if grading {
		result, err = client.GradeThread(ctx, q, messages, 1, 1)
	} else {
		result, _, err = client.EvaluateAnswer(ctx, q, messages, 3, 1, 1)
	}
	if err != nil {
		return err
	}
	if result.Score != 5 || result.MaxPoints != 10 {
		return fmt.Errorf("unexpected grade: %+v", result)
	}
	return nil
}

// TestTransientFailuresRetry проверяет повторы временных ошибок для обоих методов.
func TestTransientFailuresRetry(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rate_limit", 429, `{"error":{"message":"busy"}}`},
		{"gateway", 502, `{"error":{"message":"gateway"}}`},
		{"plain_server_error", 503, "temporarily unavailable"},
		{"empty_choices", 200, `{"choices":[]}`},
		{"invalid_envelope", 200, `{"choices":`},
		{"invalid_grade", 200, `{"choices":[{"message":{"content":"not json"}}]}`},
		{"null_grade", 200, `{"choices":[{"message":{"content":"null"}}]}`},
	}
	for _, grading := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("grade=%v/%s", grading, tc.name), func(t *testing.T) {
				var calls atomic.Int32
				client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if calls.Add(1) == 1 {
						w.WriteHeader(tc.status)
						_, _ = io.WriteString(w, tc.body)
						return
					}
					_, _ = io.WriteString(w, validGrade)
				}, WithMaxRetries(1))
				if err := callGrade(t.Context(), client, grading); err != nil {
					t.Fatal(err)
				}
				if got := calls.Load(); got != 2 {
					t.Fatalf("requests=%d, want 2", got)
				}
			})
		}
	}
}

// TestPermanentFailuresDoNotRetry отличает ошибки авторизации и запроса от временных.
func TestPermanentFailuresDoNotRetry(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"permanent"}}`)
			})
			if err := callGrade(t.Context(), client, false); err == nil {
				t.Fatal("expected error")
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("requests=%d, want 1", got)
			}
		})
	}
}

// TestRetryLimit проверяет, что число повторов не включает первоначальную попытку.
func TestRetryLimit(t *testing.T) {
	for _, retries := range []int{0, 2} {
		t.Run(fmt.Sprint(retries), func(t *testing.T) {
			var calls atomic.Int32
			client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(502) }, WithMaxRetries(retries))
			if err := callGrade(t.Context(), client, false); err == nil {
				t.Fatal("expected error")
			}
			if got := int(calls.Load()); got != retries+1 {
				t.Fatalf("requests=%d, want %d", got, retries+1)
			}
		})
	}
}

// TestTimeoutAndRecovery подтверждает ограничение зависшего запроса и повтор после тайм-аута.
func TestTimeoutAndRecovery(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			var calls atomic.Int32
			client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					_, _ = io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, validGrade)
			}, WithTimeout(80*time.Millisecond), WithMaxRetries(map[bool]int{false: 0, true: 1}[recover]))
			started := time.Now()
			err := callGrade(t.Context(), client, false)
			if recover && err != nil {
				t.Fatal(err)
			}
			if !recover && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want deadline exceeded, got %v", err)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("timeout took %v", elapsed)
			}
			if got := calls.Load(); got != map[bool]int32{false: 1, true: 2}[recover] {
				t.Fatalf("unexpected requests: %d", got)
			}
		})
	}
}

// TestRetryAfter проверяет HTTP-date, секунды и отсутствие слишком раннего повтора.
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"2", 2 * time.Second}, {now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0}, {"invalid", 0}, {"-1", 0},
		{"18446744073709551615", time.Duration(1<<63 - 1)},
		{"18446744073709551616", time.Duration(1<<63 - 1)},
		{"184467440737095516160000000000000", time.Duration(1<<63 - 1)},
		{"184467440737095516160000x", 0},
	} {
		if got := retryAfter(tc.header, now); got != tc.want {
			t.Errorf("Retry-After %q: got %v, want %v", tc.header, got, tc.want)
		}
	}
	var calls atomic.Int32
	var first atomic.Int64
	client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			first.Store(time.Now().UnixNano())
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		if elapsed := time.Since(time.Unix(0, first.Load())); elapsed < time.Second {
			t.Errorf("retried too early: %v", elapsed)
		}
		_, _ = io.WriteString(w, validGrade)
	}, WithMaxRetries(1))
	if err := callGrade(t.Context(), client, false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d, want 2", calls.Load())
	}
}

// TestCancellationDuringBackoff не допускает нового запроса после отмены ожидания Retry-After.
func TestCancellationDuringBackoff(t *testing.T) {
	var calls atomic.Int32
	client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := callGrade(ctx, client, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("backoff ignored cancellation")
	}
	if calls.Load() != 1 {
		t.Fatalf("requests=%d, want 1", calls.Load())
	}
}

// TestConcurrencyLimit запускает сто смешанных вызовов и измеряет реальную HTTP-параллельность.
func TestConcurrencyLimit(t *testing.T) {
	for _, limit := range []int{1, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			var active, peak atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			entered := make(chan struct{}, 100)
			client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
				current := active.Add(1)
				defer active.Add(-1)
				for prev := peak.Load(); current > prev; prev = peak.Load() {
					if peak.CompareAndSwap(prev, current) {
						break
					}
				}
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, validGrade)
			}, WithMaxConcurrency(limit), WithTimeout(5*time.Second), WithMaxRetries(0))
			t.Cleanup(unblock)
			results := make(chan error, 100)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			for i := 0; i < 100; i++ {
				go func() { results <- callGrade(ctx, client, i%2 == 0) }()
			}
			for i := 0; i < limit; i++ {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("concurrency slots were not filled")
				}
			}
			if got := active.Load(); got != int32(limit) {
				t.Errorf("active=%d, want %d", got, limit)
			}
			unblock()
			for i := 0; i < 100; i++ {
				if err := <-results; err != nil {
					t.Error(err)
				}
			}
			if got := peak.Load(); got > int32(limit) {
				t.Fatalf("peak=%d exceeds %d", got, limit)
			}
		})
	}
}

// TestInteractiveSlotReserved подтверждает, что очередь финальных оценок не занимает все слоты.
func TestInteractiveSlotReserved(t *testing.T) {
	entered := make(chan bool, 3)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Temperature float64 `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		entered <- (request.Temperature == 0.3)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, validGrade)
	}, WithMaxConcurrency(2), WithMaxRetries(0))
	t.Cleanup(unblock)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	results := make(chan error, 3)
	go func() { results <- callGrade(ctx, client, true) }()
	select {
	case interactive := <-entered:
		if interactive {
			t.Fatal("expected grade")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { results <- callGrade(ctx, client, true) }()
	go func() { results <- callGrade(ctx, client, false) }()
	select {
	case interactive := <-entered:
		if !interactive {
			t.Error("background work occupied the reserved slot")
		}
	case <-ctx.Done():
		t.Fatal("interactive request starved")
	}
	unblock()
	for range 3 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

// TestQueueCancellation проверяет отменяемое ожидание семафора без лишнего HTTP-запроса.
func TestQueueCancellation(t *testing.T) {
	var calls atomic.Int32
	client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, validGrade) }, WithMaxConcurrency(1), WithMaxRetries(3), WithTimeout(50*time.Millisecond))
	client.slots <- struct{}{}
	started := time.Now()
	if err := callGrade(t.Context(), client, false); !errors.Is(err, ErrQueueTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	<-client.slots
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("queue wait exhausted model retries")
	}
	if calls.Load() != 0 {
		t.Fatal("queued request reached the server")
	}
	if err := callGrade(t.Context(), client, false); err != nil {
		t.Fatalf("slot leaked: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := callGrade(ctx, client, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("canceled request reached the server")
	}
}

// TestPingTimeout распространяет ограничение времени и на проверку доступности модели.
func TestPingTimeout(t *testing.T) {
	client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, WithTimeout(50*time.Millisecond))
	if err := client.Ping(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

// TestInvalidSettings не позволяет отключить защиту ошибочными значениями настроек.
func TestInvalidSettings(t *testing.T) {
	for _, option := range []Option{WithTimeout(0), WithTimeout(-1), WithTimeout(time.Duration(1<<63 - 1)), WithMaxRetries(-1), WithMaxRetries(101), WithMaxConcurrency(0), nil} {
		if _, err := New("", "key", "model", "standard", option); err == nil {
			t.Fatal("invalid option accepted")
		}
	}
}

// TestCancellationInFlight прерывает текущий HTTP-вызов без повторов и утечки слота.
func TestCancellationInFlight(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, validGrade)
	}, WithMaxConcurrency(1))
	testCtx, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- callGrade(ctx, client, false) }()
	select {
	case <-entered:
	case <-testCtx.Done():
		t.Fatal("request did not reach the server")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-testCtx.Done():
		t.Fatal("cancellation was ignored")
	}
	if calls.Load() != 1 {
		t.Fatal("canceled request was retried")
	}
	if err := callGrade(testCtx, client, false); err != nil {
		t.Fatalf("slot leaked after cancellation: %v", err)
	}
}

// TestRetryBackoffReleasesSlot оставляет интерактивным вызовам слот во время ожидания повторов.
func TestRetryBackoffReleasesSlot(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(429)
			close(entered)
			return
		}
		_, _ = io.WriteString(w, validGrade)
	}, WithMaxConcurrency(1))
	testCtx, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- callGrade(ctx, client, true) }()
	select {
	case <-entered:
	case <-testCtx.Done():
		t.Fatal("grading request did not reach the server")
	}
	if err := callGrade(testCtx, client, false); err != nil {
		t.Fatalf("backoff held a slot: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d, want 2", calls.Load())
	}
}

// TestClientBudgetStopsLongRetryAfter отличает дедлайн клиента от защитного дедлайна теста.
func TestClientBudgetStopsLongRetryAfter(t *testing.T) {
	const timeout = 100 * time.Millisecond
	const budget = 2*timeout + 10*time.Second
	var calls atomic.Int32
	client := reliabilityClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "18446744073709551616")
		w.WriteHeader(http.StatusTooManyRequests)
	}, WithTimeout(timeout), WithMaxRetries(1))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	started := time.Now()
	err := callGrade(ctx, client, false)
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("caller safety deadline expired before client budget")
	}
	if elapsed < budget-100*time.Millisecond || elapsed > budget+time.Second {
		t.Fatalf("client budget=%v, elapsed=%v", budget, elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("huge Retry-After was retried early: %d requests", calls.Load())
	}
}
