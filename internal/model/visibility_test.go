package model

import "testing"

func TestParseScoreVisibility(t *testing.T) {
	for _, s := range []string{"live", "final", "none"} {
		if v, err := ParseScoreVisibility(s); err != nil || string(v) != s {
			t.Errorf("ParseScoreVisibility(%q) = %q, %v", s, v, err)
		}
	}
	for _, s := range []string{"", "Live", "all"} {
		if _, err := ParseScoreVisibility(s); err == nil {
			t.Errorf("ParseScoreVisibility(%q): expected error", s)
		}
	}
}

func TestResultsPolicy(t *testing.T) {
	tests := []struct {
		v      ScoreVisibility
		status SessionStatus
		want   ResultsPolicy
	}{
		{ScoreLive, StatusGraded, ResultsPolicy{LLMScores: true, Preliminary: true}},
		{ScoreLive, StatusReviewed, ResultsPolicy{LLMScores: true}},
		{ScoreFinal, StatusGraded, ResultsPolicy{LLMScores: true, Preliminary: true}},
		{ScoreFinal, StatusReviewed, ResultsPolicy{LLMScores: true}},
		{"", StatusGraded, ResultsPolicy{LLMScores: true, Preliminary: true}}, // zero value = final
		{ScoreNone, StatusGraded, ResultsPolicy{Hidden: true}},
		{ScoreNone, StatusReviewed, ResultsPolicy{}},
		{ScoreNone, StatusSubmitted, ResultsPolicy{}}, // nothing graded yet, nothing to hide
	}
	for _, tt := range tests {
		if got := tt.v.ResultsPolicy(tt.status); got != tt.want {
			t.Errorf("%q/%s: got %+v, want %+v", tt.v, tt.status, got, tt.want)
		}
	}
}

func TestShowLiveScore(t *testing.T) {
	if !ScoreLive.ShowLiveScore() || ScoreFinal.ShowLiveScore() || ScoreNone.ShowLiveScore() || ScoreVisibility("").ShowLiveScore() {
		t.Error("only live shows live scores")
	}
}
