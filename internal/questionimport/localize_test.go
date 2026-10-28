package questionimport

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
)

func ctxFor(t *testing.T, lang string) context.Context {
	t.Helper()
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}
	return i18n.WithLocalizer(context.Background(), i18n.NewLocalizer(lang))
}

func TestLocalizeRu(t *testing.T) {
	got := Localize(ctxFor(t, "ru"), []FieldError{{Question: 3, Field: "max_points", Code: "QImportErrPositive"}})
	want := "Вопрос №3, поле max_points: должно быть целым числом больше 0"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLocalizeQuestionWithoutField(t *testing.T) {
	got := Localize(ctxFor(t, "en"), []FieldError{{Question: 2, Code: "QImportErrNotObject"}})
	want := "Question 2: must be a JSON object"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLocalizeFileLevel(t *testing.T) {
	got := Localize(ctxFor(t, "en"), []FieldError{{Code: "QImportErrInvalidJSON", Params: map[string]any{"Line": 2, "Col": 5}}})
	want := "Invalid JSON at line 2, column 5."
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLocalizeWarnings(t *testing.T) {
	got := LocalizeWarnings(ctxFor(t, "en"), []Warning{{Question: 3, Code: "QImportWarnDupInFile", Params: map[string]any{"N": 3, "PrevN": 1, "Topic": "t"}}})
	want := `Question 3 duplicates question 1 in this file (topic "t"); skipped.`
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLocalesComplete(t *testing.T) {
	ids := []string{
		"QImportErrAt", "QImportErrAtQuestion", "QImportErrInvalidJSON", "QImportErrTopLevel",
		"QImportErrUnknownTopField", "QImportErrNoQuestions", "QImportErrNotObject", "QImportErrNotString",
		"QImportErrNotInteger", "QImportErrRequired", "QImportErrDifficulty", "QImportErrPositive",
		"QImportErrUnknownField", "QImportErrInternal", "QImportErrorsTitle", "QImportWarnDupInFile",
		"QImportWarnDupInDB", "QImportWarnDupOtherTopic", "QImportSummary", "QBuilderIncomplete", "QBuilderEmpty",
	}
	for _, lang := range []string{"en", "ru"} {
		data, err := os.ReadFile("../i18n/locales/active." + lang + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var msgs []struct {
			ID    string `json:"id"`
			Other string `json:"other"`
		}
		if err := json.Unmarshal(data, &msgs); err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		have := map[string]string{}
		for _, m := range msgs {
			have[m.ID] = m.Other
		}
		for _, id := range ids {
			if strings.TrimSpace(have[id]) == "" {
				t.Errorf("%s: missing message %s", lang, id)
			}
		}
	}
}
