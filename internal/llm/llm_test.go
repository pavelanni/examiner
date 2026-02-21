package llm

import (
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

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
			got := countFollowups(tt.messages)
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
		prompt := buildEvalSystemPrompt(q, true)
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
		prompt := buildEvalSystemPrompt(q, false)
		if !strings.Contains(prompt, "Do NOT ask any more follow-ups") {
			t.Error("prompt should prohibit follow-ups")
		}
		if strings.Contains(prompt, "MAY ask ONE follow-up") {
			t.Error("prompt should not allow follow-ups")
		}
	})

	t.Run("empty rubric and model answer", func(t *testing.T) {
		q2 := model.Question{Text: "Simple?", MaxPoints: 5}
		prompt := buildEvalSystemPrompt(q2, true)
		if strings.Contains(prompt, "GRADING RUBRIC") {
			t.Error("prompt should not contain rubric section when empty")
		}
		if strings.Contains(prompt, "MODEL ANSWER") {
			t.Error("prompt should not contain model answer section when empty")
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

	prompt := buildGradingSystemPrompt(q)
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
