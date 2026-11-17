package questionimport

import (
	"slices"
	"sort"

	"github.com/pavelanni/examiner/internal/model"
)

var validDifficulties = []model.Difficulty{model.DifficultyEasy, model.DifficultyMedium, model.DifficultyHard}

// Validate applies the FR1-01 rules to decoded questions. Fields that
// already failed to decode are not checked again, so each field has at
// most one error.
func Validate(drafts []Draft) []FieldError {
	var errs []FieldError
	for _, d := range drafts {
		if d.notObject {
			continue
		}
		add := func(field, code string) {
			if !d.bad[field] {
				errs = append(errs, FieldError{Question: d.Number, Field: field, Code: code})
			}
		}
		for _, f := range []struct{ field, value string }{
			{fieldText, d.Text},
			{fieldTopic, d.Topic},
			{fieldRubric, d.Rubric},
			{fieldModelAnswer, d.ModelAnswer},
		} {
			if f.value == "" {
				add(f.field, "QImportErrRequired")
			}
		}
		switch {
		case d.Difficulty == "":
			add(fieldDifficulty, "QImportErrRequired")
		case !slices.Contains(validDifficulties, d.Difficulty):
			add(fieldDifficulty, "QImportErrDifficulty")
		}
		switch {
		case !d.present[fieldMaxPoints]:
			add(fieldMaxPoints, "QImportErrRequired")
		case d.MaxPoints <= 0:
			add(fieldMaxPoints, "QImportErrPositive")
		}
	}
	return errs
}

// Parse decodes and validates a question file. The errors are sorted:
// file-level errors first, then by question number and field order.
func Parse(data []byte) ([]Draft, []FieldError) {
	drafts, errs := Decode(data)
	errs = append(errs, Validate(drafts)...)
	sortErrors(errs)
	return drafts, errs
}

func sortErrors(errs []FieldError) {
	sort.SliceStable(errs, func(i, j int) bool {
		a, b := errs[i], errs[j]
		if a.Question != b.Question {
			return a.Question < b.Question
		}
		if ra, rb := fieldRank(a.Field), fieldRank(b.Field); ra != rb {
			return ra < rb
		}
		return a.Field < b.Field
	})
}

// fieldRank orders errors without a field first, then known fields in
// fieldOrder, then unknown fields.
func fieldRank(field string) int {
	if field == "" {
		return -1
	}
	if i := slices.Index(fieldOrder, field); i >= 0 {
		return i
	}
	return len(fieldOrder)
}
