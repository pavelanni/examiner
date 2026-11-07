package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pavelanni/examiner/internal/llm"
)

// TestLLMReliabilityConfiguration проверяет приоритет CLI, окружения, YAML и значений по умолчанию.
func TestLLMReliabilityConfiguration(t *testing.T) {
	for _, source := range []string{"defaults", "file", "environment", "flags"} {
		t.Run(source, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile("examiner.yaml", []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"EXAMINER_LLM_TIMEOUT", "EXAMINER_LLM_MAX_RETRIES", "EXAMINER_LLM_MAX_CONCURRENCY"} {
				t.Setenv(key, "")
			}
			timeout, retries, concurrency := llm.DefaultTimeout, llm.DefaultMaxRetries, llm.DefaultMaxConcurrency
			if source != "defaults" {
				if err := os.WriteFile("examiner.yaml", []byte("llm-timeout: 7s\nllm-max-retries: 2\nllm-max-concurrency: 6\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				timeout, retries, concurrency = 7*time.Second, 2, 6
			}
			if source == "environment" || source == "flags" {
				t.Setenv("EXAMINER_LLM_TIMEOUT", "3s")
				t.Setenv("EXAMINER_LLM_MAX_RETRIES", "0")
				t.Setenv("EXAMINER_LLM_MAX_CONCURRENCY", "2")
				timeout, retries, concurrency = 3*time.Second, 0, 2
			}
			cmd := serveCmd()
			if source == "flags" {
				if err := cmd.ParseFlags([]string{"--llm-timeout=5s", "--llm-max-retries=1", "--llm-max-concurrency=8"}); err != nil {
					t.Fatal(err)
				}
				timeout, retries, concurrency = 5*time.Second, 1, 8
			}
			v := viperForCmd(cmd)
			if got := v.GetDuration("llm-timeout"); got != timeout {
				t.Errorf("timeout=%v, want %v", got, timeout)
			}
			if got := v.GetInt("llm-max-retries"); got != retries {
				t.Errorf("retries=%d, want %d", got, retries)
			}
			if got := v.GetInt("llm-max-concurrency"); got != concurrency {
				t.Errorf("concurrency=%d, want %d", got, concurrency)
			}
		})
	}
}

// TestInvalidRetryConfigurationFailsBeforeOpeningDatabase проверяет ошибку преобразования до изменения БД.
func TestInvalidRetryConfigurationFailsBeforeOpeningDatabase(t *testing.T) {
	for _, source := range []string{"environment", "file"} {
		t.Run(source, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("EXAMINER_LLM_TIMEOUT", "")
			t.Setenv("EXAMINER_LLM_MAX_CONCURRENCY", "")
			t.Setenv("EXAMINER_LLM_MAX_RETRIES", "")
			config := "{}\n"
			if source == "environment" {
				t.Setenv("EXAMINER_LLM_MAX_RETRIES", "three")
			} else {
				config = "llm-max-retries: three\n"
			}
			if err := os.WriteFile("examiner.yaml", []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := serveCmd()
			if err := cmd.ParseFlags([]string{"--db=must-not-exist.db"}); err != nil {
				t.Fatal(err)
			}
			if err := runServe(cmd, nil); err == nil || !strings.Contains(err.Error(), "llm-max-retries") {
				t.Fatalf("got %v", err)
			}
			if _, err := os.Stat("must-not-exist.db"); !os.IsNotExist(err) {
				t.Fatalf("bad configuration touched the database: %v", err)
			}
			if err := cmd.ParseFlags([]string{"--llm-max-retries=0"}); err != nil {
				t.Fatal(err)
			}
			if _, err := llmOptions(viperForCmd(cmd)); err != nil {
				t.Fatalf("valid flag must override invalid lower-priority config: %v", err)
			}
		})
	}
}
