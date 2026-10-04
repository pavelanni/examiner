package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

func (e *answerEnv) get(t *testing.T, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %q", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// The fake LLM scores every answer 1 out of 10.
const liveScore = "1.0 / 10"

func TestLiveScoreOnlyInLiveMode(t *testing.T) {
	tests := []struct {
		vis  model.ScoreVisibility
		want bool
	}{
		{model.ScoreLive, true},
		{model.ScoreFinal, false},
		{model.ScoreNone, false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.vis), func(t *testing.T) {
			env := newAnswerEnv(t, 3, &fakeLLM{})
			env.h.config.ScoreVisibility = tt.vis

			rec := env.post("my answer")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
			}
			reply := rec.Body.String()
			page := env.get(t, fmt.Sprintf("/exam/%d", env.sessID)) // after a reload

			for name, body := range map[string]string{"answer reply": reply, "exam page": page} {
				if got := strings.Contains(body, liveScore); got != tt.want {
					t.Errorf("%s contains score = %v, want %v", name, got, tt.want)
				}
			}
			if tt.want && !strings.Contains(reply, "preliminary") {
				t.Error("live score should be labeled preliminary")
			}
			// The score is stored either way; only display is gated.
			msgs, _ := env.store.GetMessages(env.threadID)
			last := msgs[len(msgs)-1]
			if last.Score == nil || *last.Score != 1 {
				t.Errorf("stored score = %v, want 1", last.Score)
			}
		})
	}
}

const (
	llmScoreMark    = "7.5 / 10"
	llmFeedbackMark = "SECRETFEEDBACK"
	llmGradeMark    = "75.0%"
	finalGradeMark  = "80.0%"
	teacherMark     = "6.0 / 10"
	awaitingMark    = "after a teacher reviews"
)

// gradedEnv returns an environment whose session is graded, with scores saved.
func gradedEnv(t *testing.T, vis model.ScoreVisibility) *answerEnv {
	t.Helper()
	env := newAnswerEnv(t, 3, &fakeLLM{})
	env.h.config.ScoreVisibility = vis
	if err := env.store.UpsertScore(model.QuestionScore{ThreadID: env.threadID, LLMScore: 7.5, LLMFeedback: llmFeedbackMark}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertGrade(model.Grade{SessionID: env.sessID, LLMGrade: 75}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpdateSessionStatus(env.sessID, model.StatusGraded); err != nil {
		t.Fatal(err)
	}
	return env
}

func review(t *testing.T, env *answerEnv) {
	t.Helper()
	if err := env.store.UpdateTeacherScore(env.threadID, 6, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.FinalizeGrade(env.sessID, 80, 1); err != nil {
		t.Fatal(err)
	}
	// handleFinalizeGrade does this next.
	if err := env.store.UpdateSessionStatus(env.sessID, model.StatusReviewed); err != nil {
		t.Fatal(err)
	}
}

func TestResultsVisibility(t *testing.T) {
	results := func(env *answerEnv) string { return env.get(t, fmt.Sprintf("/results/%d", env.sessID)) }

	for _, vis := range []model.ScoreVisibility{model.ScoreLive, model.ScoreFinal, ""} {
		t.Run("graded/"+string(vis), func(t *testing.T) {
			body := results(gradedEnv(t, vis))
			for _, want := range []string{llmScoreMark, llmFeedbackMark, llmGradeMark, "preliminary"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q", want)
				}
			}
		})
	}

	t.Run("graded/none hides everything", func(t *testing.T) {
		body := results(gradedEnv(t, model.ScoreNone))
		if !strings.Contains(body, awaitingMark) {
			t.Errorf("missing awaiting-review notice")
		}
		for _, leak := range []string{llmScoreMark, llmFeedbackMark, llmGradeMark} {
			if strings.Contains(body, leak) {
				t.Errorf("leaked %q before review", leak)
			}
		}
	})

	t.Run("reviewed/none shows teacher result only", func(t *testing.T) {
		env := gradedEnv(t, model.ScoreNone)
		review(t, env)
		body := results(env)
		for _, want := range []string{finalGradeMark, teacherMark} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q", want)
			}
		}
		for _, leak := range []string{llmScoreMark, llmFeedbackMark, llmGradeMark, awaitingMark} {
			if strings.Contains(body, leak) {
				t.Errorf("unexpected %q", leak)
			}
		}
	})

	t.Run("reviewed/final is no longer preliminary", func(t *testing.T) {
		env := gradedEnv(t, model.ScoreFinal)
		review(t, env)
		body := results(env)
		if !strings.Contains(body, finalGradeMark) || !strings.Contains(body, llmScoreMark) {
			t.Error("expected final grade and LLM score")
		}
		if strings.Contains(body, "preliminary") {
			t.Error("reviewed results must not be labeled preliminary")
		}
	})
}
