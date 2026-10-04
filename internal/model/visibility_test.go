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
		{ScoreLive, StatusGraded, ResultsPolicy{LLMResults: true, Preliminary: true}},
		{ScoreLive, StatusReviewed, ResultsPolicy{LLMResults: true}},
		{ScoreFinal, StatusGraded, ResultsPolicy{LLMResults: true, Preliminary: true}},
		{ScoreFinal, StatusReviewed, ResultsPolicy{LLMResults: true}},
		{"", StatusGraded, ResultsPolicy{LLMResults: true, Preliminary: true}}, // zero value = final
		{ScoreNone, StatusGraded, ResultsPolicy{Hidden: true}},
		{ScoreNone, StatusReviewed, ResultsPolicy{}},
		{ScoreNone, StatusGrading, ResultsPolicy{Hidden: true}},
		{ScoreNone, StatusSubmitted, ResultsPolicy{Hidden: true}},
	}
	for _, tt := range tests {
		if got := tt.v.ResultsPolicy(tt.status); got != tt.want {
			t.Errorf("%q/%s: got %+v, want %+v", tt.v, tt.status, got, tt.want)
		}
	}
}

func TestLiveScorePolicy(t *testing.T) {
	tests := []struct {
		v      ScoreVisibility
		status SessionStatus
		want   LiveScorePolicy
	}{
		{ScoreLive, StatusInProgress, LiveScorePolicy{Show: true, Preliminary: true}},
		{ScoreLive, StatusGraded, LiveScorePolicy{Show: true, Preliminary: true}},
		{ScoreLive, StatusReviewed, LiveScorePolicy{Show: true}}, // label drops after review
		{ScoreFinal, StatusInProgress, LiveScorePolicy{Preliminary: true}},
		{ScoreNone, StatusInProgress, LiveScorePolicy{Preliminary: true}},
		{"", StatusInProgress, LiveScorePolicy{Preliminary: true}},
	}
	for _, tt := range tests {
		if got := tt.v.LiveScorePolicy(tt.status); got != tt.want {
			t.Errorf("%q/%s: got %+v, want %+v", tt.v, tt.status, got, tt.want)
		}
	}
}
