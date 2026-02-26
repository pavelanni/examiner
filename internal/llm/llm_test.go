package llm

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/llm/prompts"
	"github.com/pavelanni/examiner/internal/model"
)

func TestMain(m *testing.M) {
	if err := prompts.Load(promptsFS); err != nil {
		fmt.Fprintf(os.Stderr, "prompts.Load failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestCountFollowups(t *testing.T) {
	tests := []struct {
		name     string
		messages []model.Message
		want     int
	}{
		{"empty", nil, 0},
		{"no LLM messages", []model.Message{
			{Role: model.RoleStudent, Content: "answer"},
			{Role: model.RoleStudent, Content: "more"},
		}, 0},
		{"all LLM", []model.Message{
			{Role: model.RoleLLM, Content: "q1"},
			{Role: model.RoleLLM, Content: "q2"},
		}, 2},
		{"mixed", []model.Message{
			{Role: model.RoleStudent, Content: "a1"},
			{Role: model.RoleLLM, Content: "q1"},
			{Role: model.RoleStudent, Content: "a2"},
			{Role: model.RoleLLM, Content: "q2"},
		}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prompts.CountFollowups(tt.messages)
			if got != tt.want {
				t.Errorf("countFollowups() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBuildEvalSystemPrompt(t *testing.T) {
	q := model.Question{
		Text:        "What is a goroutine?",
		Rubric:      "Must mention lightweight thread",
		ModelAnswer: "A goroutine is a lightweight thread managed by Go runtime.",
		MaxPoints:   10,
	}

	t.Run("can followup", func(t *testing.T) {
		prompt, err := prompts.BuildEvalPrompt(prompts.PromptStandard, q, []model.Message{
			{Role: model.RoleStudent, Content: "answer"},
		}, 3)
		if err != nil {
			t.Fatalf("failed to build prompt: %v", err)
		}
		if !strings.Contains(prompt, q.Text) {
			t.Error("prompt should contain question text")
		}
		if !strings.Contains(prompt, q.Rubric) {
			t.Error("prompt should contain rubric")
		}
		if !strings.Contains(prompt, q.ModelAnswer) {
			t.Error("prompt should contain model answer")
		}
		if !strings.Contains(prompt, "MAY ask ONE follow-up") {
			t.Error("prompt should allow follow-ups")
		}
		if strings.Contains(prompt, "Do NOT ask any more follow-ups") {
			t.Error("prompt should not prohibit follow-ups")
		}
	})

	t.Run("cannot followup", func(t *testing.T) {
		messages := []model.Message{
			{Role: model.RoleStudent, Content: "a1"},
			{Role: model.RoleLLM, Content: "q1"},
			{Role: model.RoleStudent, Content: "a2"},
			{Role: model.RoleLLM, Content: "q2"},
			{Role: model.RoleStudent, Content: "a3"},
			{Role: model.RoleLLM, Content: "q3"},
		}
		prompt, err := prompts.BuildEvalPrompt(prompts.PromptStandard, q, messages, 3)
		if err != nil {
			t.Fatalf("failed to build prompt: %v", err)
		}
		if !strings.Contains(prompt, "Do NOT ask any more follow-ups") {
			t.Error("prompt should prohibit follow-ups")
		}
		if strings.Contains(prompt, "MAY ask ONE follow-up") {
			t.Error("prompt should not allow follow-ups")
		}
	})

	t.Run("empty rubric and model answer", func(t *testing.T) {
		q2 := model.Question{Text: "Simple?", MaxPoints: 5}
		prompt, err := prompts.BuildEvalPrompt(prompts.PromptStandard, q2, []model.Message{
			{Role: model.RoleStudent, Content: "answer"},
		}, 3)
		if err != nil {
			t.Fatalf("failed to build prompt: %v", err)
		}
		if strings.Contains(prompt, "Must mention") {
			t.Error("prompt should not contain rubric content when empty")
		}
		if strings.Contains(prompt, "lightweight thread") {
			t.Error("prompt should not contain model answer content when empty")
		}
	})
}

func TestBuildGradingSystemPrompt(t *testing.T) {
	q := model.Question{
		Text:        "Explain channels",
		Rubric:      "Mention typed conduit",
		ModelAnswer: "Channels are typed conduits for goroutine communication.",
		MaxPoints:   10,
	}

	messages := []model.Message{
		{Role: model.RoleStudent, Content: "answer"},
		{Role: model.RoleLLM, Content: "followup"},
		{Role: model.RoleStudent, Content: "response"},
	}

	prompt, err := prompts.BuildGradePrompt(prompts.PromptStandard, q, messages)
	if err != nil {
		t.Fatalf("failed to build prompt: %v", err)
	}
	if !strings.Contains(prompt, q.Text) {
		t.Error("prompt should contain question text")
	}
	if !strings.Contains(prompt, q.Rubric) {
		t.Error("prompt should contain rubric")
	}
	if !strings.Contains(prompt, q.ModelAnswer) {
		t.Error("prompt should contain model answer")
	}
	if !strings.Contains(prompt, `"need_followup": false`) {
		t.Error("grading prompt should always set need_followup false")
	}
}
