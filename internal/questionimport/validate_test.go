package questionimport

import (
	"strings"
	"testing"
)

// question builds a one-question array, replacing the named field's raw
// JSON value; an empty value removes the field.
func question(field, value string) string {
	fields := []struct{ k, v string }{
		{"text", `"Q"`}, {"difficulty", `"easy"`}, {"topic", `"t"`},
		{"rubric", `"r"`}, {"model_answer", `"a"`}, {"max_points", `10`},
	}
	var parts []string
	for _, f := range fields {
		v := f.v
		if f.k == field {
			v = value
		}
		if v == "" {
			continue
		}
		parts = append(parts, `"`+f.k+`":`+v)
	}
	return `[{` + strings.Join(parts, ",") + `}]`
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []errKey
	}{
		{"valid", question("", ""), nil},
		{"text empty", question("text", `""`), []errKey{{1, "text", "QImportErrRequired"}}},
		{"text whitespace", question("text", `"   "`), []errKey{{1, "text", "QImportErrRequired"}}},
		{"text missing", question("text", ""), []errKey{{1, "text", "QImportErrRequired"}}},
		{"topic empty", question("topic", `""`), []errKey{{1, "topic", "QImportErrRequired"}}},
		{"rubric missing", question("rubric", ""), []errKey{{1, "rubric", "QImportErrRequired"}}},
		{"rubric null", question("rubric", "null"), []errKey{{1, "rubric", "QImportErrRequired"}}},
		{"model answer empty", question("model_answer", `" "`), []errKey{{1, "model_answer", "QImportErrRequired"}}},
		{"difficulty missing", question("difficulty", ""), []errKey{{1, "difficulty", "QImportErrRequired"}}},
		{"difficulty wrong case", question("difficulty", `"Easy"`), []errKey{{1, "difficulty", "QImportErrDifficulty"}}},
		{"difficulty unknown", question("difficulty", `"extreme"`), []errKey{{1, "difficulty", "QImportErrDifficulty"}}},
		{"points missing", question("max_points", ""), []errKey{{1, "max_points", "QImportErrRequired"}}},
		{"points zero", question("max_points", "0"), []errKey{{1, "max_points", "QImportErrPositive"}}},
		{"points negative", question("max_points", "-1"), []errKey{{1, "max_points", "QImportErrPositive"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drafts, derrs := Decode([]byte(tt.input))
			if len(derrs) != 0 {
				t.Fatalf("decode errors: %v", derrs)
			}
			if got := keys(Validate(drafts)); !sameKeys(got, tt.want) {
				t.Errorf("errors = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseOrdering(t *testing.T) {
	input := `{"questions":[` +
		`{"text":"Q","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":10,"zz":1},` +
		`{"text":"","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":0}` +
		`],"extra":true}`
	_, errs := Parse([]byte(input))
	want := []errKey{
		{0, "extra", "QImportErrUnknownTopField"},
		{1, "zz", "QImportErrUnknownField"},
		{2, "text", "QImportErrRequired"},
		{2, "max_points", "QImportErrPositive"},
	}
	if got := keys(errs); !sameKeys(got, want) {
		t.Errorf("errors = %v, want %v", got, want)
	}
}

func TestParseNoDoubleError(t *testing.T) {
	_, errs := Parse([]byte(question("max_points", `"5"`)))
	want := []errKey{{1, "max_points", "QImportErrNotInteger"}}
	if got := keys(errs); !sameKeys(got, want) {
		t.Errorf("errors = %v, want %v", got, want)
	}
}

func TestParseSkipsNonObjectQuestion(t *testing.T) {
	_, errs := Parse([]byte(`[1]`))
	want := []errKey{{1, "", "QImportErrNotObject"}}
	if got := keys(errs); !sameKeys(got, want) {
		t.Errorf("errors = %v, want %v", got, want)
	}
}
