package model

import "fmt"

// ScoreVisibility controls when a student sees AI-generated scores.
// The zero value behaves like ScoreFinal.
type ScoreVisibility string

const (
	// ScoreLive shows a per-answer score during the exam, plus the final grade.
	ScoreLive ScoreVisibility = "live"
	// ScoreFinal shows scores only on the results page, once graded.
	ScoreFinal ScoreVisibility = "final"
	// ScoreNone shows nothing until the teacher has finalized the grade, and
	// never shows AI scores.
	ScoreNone ScoreVisibility = "none"
)

// ParseScoreVisibility validates a configuration value.
func ParseScoreVisibility(s string) (ScoreVisibility, error) {
	switch v := ScoreVisibility(s); v {
	case ScoreLive, ScoreFinal, ScoreNone:
		return v, nil
	}
	return "", fmt.Errorf("invalid score visibility %q (want live, final or none)", s)
}

// ShowLiveScore reports whether each LLM reply shows its score during the exam.
func (v ScoreVisibility) ShowLiveScore() bool {
	return v == ScoreLive
}

// ResultsPolicy says what the student's results page may show.
type ResultsPolicy struct {
	// Hidden withholds all grades: the teacher has not finalized yet.
	Hidden bool
	// LLMScores shows the LLM's scores, feedback and suggested grade.
	LLMScores bool
	// Preliminary labels shown scores as not yet reviewed by a teacher.
	Preliminary bool
}

// ResultsPolicy returns the rules for a session in the given status.
func (v ScoreVisibility) ResultsPolicy(status SessionStatus) ResultsPolicy {
	if v == ScoreNone {
		// Teacher scores and the final grade appear only after review.
		return ResultsPolicy{Hidden: status == StatusGraded}
	}
	return ResultsPolicy{LLMScores: true, Preliminary: status != StatusReviewed}
}
