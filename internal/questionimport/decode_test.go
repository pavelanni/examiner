package questionimport

import (
	"strings"
	"testing"
)

// validQ returns one valid question object with the given text.
func validQ(text string) string {
	return `{"text":"` + text + `","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":10}`
}

// errKey is the comparable part of a FieldError.
type errKey struct {
	Question int
	Field    string
	Code     string
}

func keys(errs []FieldError) []errKey {
	out := make([]errKey, 0, len(errs))
	for _, e := range errs {
		out = append(out, errKey{e.Question, e.Field, e.Code})
	}
	return out
}

func sameKeys(got, want []errKey) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		drafts int
		want   []errKey
	}{
		{"array", `[` + validQ("Q") + `]`, 1, nil},
		{"wrapper", `{"test_name":"x","test_description":"d","default_topic":"t","questions":[` + validQ("Q") + `]}`, 1, nil},
		{"wrapper unknown key", `{"questions":[` + validQ("Q") + `],"extra":1}`, 1,
			[]errKey{{0, "extra", "QImportErrUnknownTopField"}}},
		{"top level string", `"str"`, 0, []errKey{{0, "", "QImportErrTopLevel"}}},
		{"top level number", `42`, 0, []errKey{{0, "", "QImportErrTopLevel"}}},
		{"empty array", `[]`, 0, []errKey{{0, "", "QImportErrNoQuestions"}}},
		{"empty wrapper", `{"questions":[]}`, 0, []errKey{{0, "", "QImportErrNoQuestions"}}},
		{"wrapper without questions", `{"test_name":"x"}`, 0, []errKey{{0, "", "QImportErrNoQuestions"}}},
		{"invalid json", "[\n{\"text\": }", 0, []errKey{{0, "", "QImportErrInvalidJSON"}}},
		{"not an object", `[1]`, 1, []errKey{{1, "", "QImportErrNotObject"}}},
		{"points as string", `[{"text":"Q","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":"5"}]`, 1,
			[]errKey{{1, "max_points", "QImportErrNotInteger"}}},
		{"points fractional", `[{"text":"Q","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":2.5}]`, 1,
			[]errKey{{1, "max_points", "QImportErrNotInteger"}}},
		{"points 10.0", `[{"text":"Q","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":10.0}]`, 1,
			[]errKey{{1, "max_points", "QImportErrNotInteger"}}},
		{"points exponent", `[{"text":"Q","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":1e1}]`, 1,
			[]errKey{{1, "max_points", "QImportErrNotInteger"}}},
		{"text not string", `[{"text":5,"difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":10}]`, 1,
			[]errKey{{1, "text", "QImportErrNotString"}}},
		{"unknown field", `[{"text":"Q","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":10,"foo":1}]`, 1,
			[]errKey{{1, "foo", "QImportErrUnknownField"}}},
		{"errors in two questions", `[` + validQ("A") + `,{"text":5,"difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":10},` +
			validQ("C") + `,{"text":"D","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":"x"}]`, 4,
			[]errKey{{2, "text", "QImportErrNotString"}, {4, "max_points", "QImportErrNotInteger"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drafts, errs := Decode([]byte(tt.input))
			if len(drafts) != tt.drafts {
				t.Errorf("drafts = %d, want %d", len(drafts), tt.drafts)
			}
			if got := keys(errs); !sameKeys(got, tt.want) {
				t.Errorf("errors = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeNumbersAndTrims(t *testing.T) {
	drafts, errs := Decode([]byte(`[` + validQ("A") + `,{"text":"  Q  ","difficulty":" easy ","topic":" t ","rubric":" r ","model_answer":" a ","max_points":3}]`))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	d := drafts[1]
	if d.Number != 2 {
		t.Errorf("Number = %d, want 2", d.Number)
	}
	if d.Text != "Q" || d.Topic != "t" || d.Rubric != "r" || d.ModelAnswer != "a" || d.Difficulty != "easy" {
		t.Errorf("values not trimmed: %+v", d.QuestionImport)
	}
	if d.MaxPoints != 3 {
		t.Errorf("MaxPoints = %d, want 3", d.MaxPoints)
	}
}

func TestDecodeKeepsReadableValues(t *testing.T) {
	drafts, _ := Decode([]byte(`[{"text":"Q","difficulty":"easy","topic":"t","rubric":"r","model_answer":"a","max_points":"5"}]`))
	if len(drafts) != 1 || drafts[0].Text != "Q" || drafts[0].MaxPoints != 0 {
		t.Errorf("drafts = %+v, want text kept and max_points 0", drafts)
	}
}

func TestDecodeInvalidJSONPosition(t *testing.T) {
	_, errs := Decode([]byte("[\n{\"text\": }"))
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want 1", errs)
	}
	if errs[0].Params["Line"] != 2 {
		t.Errorf("Line = %v, want 2", errs[0].Params["Line"])
	}
	if col, ok := errs[0].Params["Col"].(int); !ok || col < 1 {
		t.Errorf("Col = %v, want a positive int", errs[0].Params["Col"])
	}
}

func TestDecodeBOM(t *testing.T) {
	drafts, errs := Decode([]byte("\xEF\xBB\xBF[" + validQ("Q") + "]"))
	if len(errs) != 0 || len(drafts) != 1 {
		t.Errorf("drafts = %d, errors = %v; want 1 draft, no errors", len(drafts), errs)
	}
}

func TestDecodeNull(t *testing.T) {
	drafts, errs := Decode([]byte(`[{"text":"Q","difficulty":"easy","topic":"t","rubric":null,"model_answer":"a","max_points":null}]`))
	if len(errs) != 0 {
		t.Fatalf("null must not be a decode error, got %v", errs)
	}
	if drafts[0].present["rubric"] || drafts[0].present["max_points"] {
		t.Errorf("null fields marked present: %v", drafts[0].present)
	}
	if !drafts[0].present["text"] {
		t.Errorf("text not marked present")
	}
}

func TestDecodeUnknownFieldName(t *testing.T) {
	_, errs := Decode([]byte(`[{"text":"Q","zzz":1}]`))
	found := false
	for _, e := range errs {
		if e.Field == "zzz" && strings.HasPrefix(e.Code, "QImportErrUnknown") {
			found = true
		}
	}
	if !found {
		t.Errorf("unknown field zzz not reported: %v", errs)
	}
}
