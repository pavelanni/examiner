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

// LiveScorePolicy says whether each LLM reply on the exam page shows its score.
type LiveScorePolicy struct {
	Show bool
	// Preliminary labels the score as not yet reviewed by a teacher.
	Preliminary bool
}

// LiveScorePolicy returns the per-answer score rules for a session in the
// given status.
func (v ScoreVisibility) LiveScorePolicy(status SessionStatus) LiveScorePolicy {
	return LiveScorePolicy{Show: v == ScoreLive, Preliminary: status != StatusReviewed}
}

// ResultsPolicy says what the student's results page may show.
type ResultsPolicy struct {
	// Hidden withholds all grades: the teacher has not finalized yet.
	Hidden bool
	// LLMResults shows the LLM's per-question scores and feedback and its
	// suggested grade.
	LLMResults bool
	// Preliminary labels shown scores as not yet reviewed by a teacher.
	Preliminary bool
}

// ResultsPolicy returns the rules for a session in the given status.
func (v ScoreVisibility) ResultsPolicy(status SessionStatus) ResultsPolicy {
	if v == ScoreNone {
		// Teacher scores and the final grade appear only after review.
		return ResultsPolicy{Hidden: status != StatusReviewed}
	}
	return ResultsPolicy{LLMResults: true, Preliminary: status != StatusReviewed}
}
