package main

import (
	"fmt"

	"github.com/pavelanni/examiner/internal/model"
)

// exportQuestionCount derives metadata from actual attempts. The export format
// has one exam-wide count, so mixed-size attempts must not be silently mislabeled.
func exportQuestionCount(results []model.StudentResult, preparedCount int) (int, error) {
	if len(results) == 0 {
		return preparedCount, nil
	}
	n := len(results[0].Questions)
	for i, result := range results {
		if len(result.Questions) != n {
			return 0, fmt.Errorf("export: session result %d has %d questions, expected %d; mixed question counts cannot share one exam export", i+1, len(result.Questions), n)
		}
	}
	return n, nil
}
