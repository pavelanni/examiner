// Package questionimport decodes, validates and imports question files.
// It is the single source of the question rules (FR1-01) and of the
// all-or-nothing import order (FR1-02) for every entry point: the admin
// import, the teacher constructor and the --questions startup load.
package questionimport

import "github.com/pavelanni/examiner/internal/model"

// Question field names in the order errors are reported.
const (
	fieldText        = "text"
	fieldDifficulty  = "difficulty"
	fieldTopic       = "topic"
	fieldRubric      = "rubric"
	fieldModelAnswer = "model_answer"
	fieldMaxPoints   = "max_points"
)

var fieldOrder = []string{fieldText, fieldDifficulty, fieldTopic, fieldRubric, fieldModelAnswer, fieldMaxPoints}

// Draft is one decoded question. Values that could be read are kept even
// when another field has the wrong type, so the constructor can show them again.
type Draft struct {
	Number int // 1-based position in the file
	model.QuestionImport

	present   map[string]bool // JSON keys that were present and not null
	bad       map[string]bool // fields that already have a decode error
	notObject bool            // the question was not a JSON object; nothing to validate
}

// FieldError describes one problem in a question file. Question == 0 means
// the file as a whole; Field is empty for errors not tied to one field.
type FieldError struct {
	Question int
	Field    string
	Code     string // i18n message ID
	Params   map[string]any
}
