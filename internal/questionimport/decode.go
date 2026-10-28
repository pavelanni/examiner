package questionimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/pavelanni/examiner/internal/model"
)

var utf8BOM = []byte("\xEF\xBB\xBF")

// wrapperKeys are the keys allowed in the {"questions": [...]} format.
// Everything except "questions" is accepted and ignored.
var wrapperKeys = map[string]bool{
	"questions":        true,
	"test_name":        true,
	"test_description": true,
	"default_topic":    true,
}

// Decode parses a question file: either a JSON array of questions or an
// object with a "questions" array. It never stops at the first problem:
// it returns every question it could read and every error it found.
// String values are trimmed.
func Decode(data []byte) ([]Draft, []FieldError) {
	data = bytes.TrimPrefix(data, utf8BOM)

	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			line, col := position(data, syn.Offset)
			return nil, []FieldError{{Code: "QImportErrInvalidJSON", Params: map[string]any{"Line": line, "Col": col}}}
		}
		return nil, []FieldError{{Code: "QImportErrInvalidJSON", Params: map[string]any{"Line": 1, "Col": 1}}}
	}

	var errs []FieldError
	var items []json.RawMessage
	switch firstByte(raw) {
	case '[':
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, []FieldError{{Code: "QImportErrTopLevel"}}
		}
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, []FieldError{{Code: "QImportErrTopLevel"}}
		}
		for _, k := range sortedKeys(obj) {
			if !wrapperKeys[k] {
				errs = append(errs, FieldError{Field: k, Code: "QImportErrUnknownTopField", Params: map[string]any{"Field": k}})
			}
		}
		if q, ok := obj["questions"]; ok && firstByte(q) != 'n' {
			if firstByte(q) != '[' {
				return nil, append(errs, FieldError{Code: "QImportErrTopLevel"})
			}
			if err := json.Unmarshal(q, &items); err != nil {
				return nil, append(errs, FieldError{Code: "QImportErrTopLevel"})
			}
		}
	default:
		return nil, []FieldError{{Code: "QImportErrTopLevel"}}
	}

	if len(items) == 0 {
		return nil, append(errs, FieldError{Code: "QImportErrNoQuestions"})
	}

	drafts := make([]Draft, 0, len(items))
	for i, item := range items {
		d, derrs := decodeQuestion(i+1, item)
		drafts = append(drafts, d)
		errs = append(errs, derrs...)
	}
	return drafts, errs
}

func decodeQuestion(n int, raw json.RawMessage) (Draft, []FieldError) {
	d := Draft{Number: n, present: map[string]bool{}, bad: map[string]bool{}}
	if firstByte(raw) != '{' {
		return d, []FieldError{{Question: n, Code: "QImportErrNotObject"}}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return d, []FieldError{{Question: n, Code: "QImportErrNotObject"}}
	}

	var errs []FieldError
	fail := func(field, code string) {
		d.bad[field] = true
		errs = append(errs, FieldError{Question: n, Field: field, Code: code})
	}

	strField := func(field string, dst *string) {
		v, ok := obj[field]
		if !ok || isNull(v) {
			return
		}
		d.present[field] = true
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			fail(field, "QImportErrNotString")
			return
		}
		*dst = strings.TrimSpace(s)
	}

	var difficulty string
	strField(fieldText, &d.Text)
	strField(fieldDifficulty, &difficulty)
	strField(fieldTopic, &d.Topic)
	strField(fieldRubric, &d.Rubric)
	strField(fieldModelAnswer, &d.ModelAnswer)
	d.Difficulty = model.Difficulty(difficulty)

	if v, ok := obj[fieldMaxPoints]; ok && !isNull(v) {
		d.present[fieldMaxPoints] = true
		if p, ok := parseInt(v); ok {
			d.MaxPoints = p
		} else {
			fail(fieldMaxPoints, "QImportErrNotInteger")
		}
	}

	for _, k := range sortedKeys(obj) {
		if !slices.Contains(fieldOrder, k) {
			fail(k, "QImportErrUnknownField")
		}
	}
	return d, errs
}

// parseInt accepts only a JSON number written as an integer literal:
// "5", 2.5, 10.0 and 1e1 are all rejected.
func parseInt(v json.RawMessage) (int, bool) {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return 0, false
	}
	num, ok := x.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(num.String(), 10, 0)
	if err != nil {
		return 0, false
	}
	return int(i), true
}

func firstByte(v []byte) byte {
	v = bytes.TrimLeft(v, " \t\r\n")
	if len(v) == 0 {
		return 0
	}
	return v[0]
}

func isNull(v json.RawMessage) bool {
	return string(bytes.TrimSpace(v)) == "null"
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// position converts a byte offset into a 1-based line and column.
func position(data []byte, offset int64) (line, col int) {
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	before := data[:offset]
	line = bytes.Count(before, []byte("\n")) + 1
	col = len(before) - bytes.LastIndexByte(before, '\n')
	return line, col
}
