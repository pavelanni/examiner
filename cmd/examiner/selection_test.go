package main

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/pavelanni/examiner/internal/model"
)

// clearSelectionEnvironment isolates absence from explicitly empty settings.
func clearSelectionEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"EXAMINER_NUM_QUESTIONS", "EXAMINER_DIFFICULTY", "EXAMINER_QUESTION_SELECTION"} {
		previous, present := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

// configFrom reads a YAML fixture through the production config decoder.
func configFrom(t *testing.T, doc string) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(doc)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	return v
}

// TestExamSelection checks distributions and explicit legacy-option conflicts.
func TestExamSelection(t *testing.T) {
	clearSelectionEnvironment(t)
	tests := []struct {
		name    string
		doc     string
		want    model.Distribution
		wantErr string
	}{
		{name: "not set", doc: "num-questions: 3\ndifficulty: easy\n"},
		{name: "explicit null", doc: "question_selection: null\n", wantErr: "null or empty values are not allowed"},
		{name: "explicit empty", doc: "question_selection:\n", wantErr: "null or empty values are not allowed"},
		{name: "set", doc: "question_selection:\n  easy: 2\n  medium: 2\n  hard: 1\n",
			want: model.Distribution{"easy": 2, "medium": 2, "hard": 1}},
		{name: "explicit zero conflicts", doc: "num-questions: 0\nquestion_selection:\n  hard: 1\n",
			wantErr: "cannot be combined with num-questions or difficulty"},
		{name: "empty difficulty conflicts", doc: "difficulty: \"\"\nquestion_selection:\n  hard: 1\n",
			wantErr: "cannot be combined with num-questions or difficulty"},
		{name: "invalid legacy count conflicts", doc: "num-questions: abc\nquestion_selection:\n  hard: 1\n",
			wantErr: "cannot be combined with num-questions or difficulty"},
		{name: "conflicts with num-questions", doc: "num-questions: 5\nquestion_selection:\n  easy: 1\n",
			wantErr: "cannot be combined with num-questions or difficulty"},
		{name: "conflicts with difficulty", doc: "difficulty: easy,medium\nquestion_selection:\n  easy: 1\n",
			wantErr: "cannot be combined with num-questions or difficulty"},
		{name: "invalid value", doc: "question_selection:\n  hard: 1.5\n",
			wantErr: "question_selection: hard: must be a whole number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := examSelection(configFrom(t, tt.doc))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, n := range tt.want {
				if got[k] != n {
					t.Errorf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestExamSelectionConfigurationSources distinguishes defaults and explicit settings.
func TestExamSelectionConfigurationSources(t *testing.T) {
	for _, source := range []string{"defaults", "flag zero", "flag empty", "environment zero", "environment empty difficulty", "environment empty count"} {
		t.Run(source, func(t *testing.T) {
			t.Chdir(t.TempDir())
			clearSelectionEnvironment(t)
			if err := os.WriteFile("examiner.yaml", []byte("question_selection:\n  hard: 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := serveCmd()
			var flags []string
			switch source {
			case "flag zero":
				flags = []string{"--num-questions=0"}
			case "flag empty":
				flags = []string{"--difficulty="}
			case "environment zero":
				t.Setenv("EXAMINER_NUM_QUESTIONS", "0")
			case "environment empty difficulty":
				t.Setenv("EXAMINER_DIFFICULTY", "")
			case "environment empty count":
				t.Setenv("EXAMINER_NUM_QUESTIONS", "")
			}
			if err := cmd.ParseFlags(flags); err != nil {
				t.Fatal(err)
			}
			d, err := examSelection(viperForCmd(cmd))
			if source == "defaults" {
				if err != nil || d[model.DifficultyHard] != 1 {
					t.Fatalf("unchanged flag defaults must allow a distribution: %v, %v", d, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
				t.Fatalf("explicit legacy setting must conflict: %v, %v", d, err)
			}
		})
	}
}

// TestExamSelectionFailsBeforeOpeningDatabase prevents invalid settings from creating a DB.
func TestExamSelectionFailsBeforeOpeningDatabase(t *testing.T) {
	for _, doc := range []string{
		"question_selection:\n  easy: 1.5\n",
		"question_selection: null\n",
		"question_selection:\n",
		"num-questions: 0\nquestion_selection:\n  hard: 1\n",
	} {
		t.Run(doc, func(t *testing.T) {
			t.Chdir(t.TempDir())
			clearSelectionEnvironment(t)
			for _, key := range []string{"EXAMINER_LLM_TIMEOUT", "EXAMINER_LLM_MAX_RETRIES", "EXAMINER_LLM_MAX_CONCURRENCY"} {
				t.Setenv(key, "")
			}
			if err := os.WriteFile("examiner.yaml", []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := serveCmd()
			if err := cmd.ParseFlags([]string{"--db=must-not-exist.db"}); err != nil {
				t.Fatal(err)
			}
			if err := runServe(cmd, nil); err == nil || !strings.Contains(err.Error(), "question_selection") {
				t.Fatalf("expected selection startup error: %v", err)
			}
			if _, err := os.Stat("must-not-exist.db"); !os.IsNotExist(err) {
				t.Fatalf("invalid selection touched the database: %v", err)
			}
		})
	}
}
