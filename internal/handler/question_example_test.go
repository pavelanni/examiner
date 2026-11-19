package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pavelanni/examiner/internal/questionimport"
)

func TestQuestionExampleValid(t *testing.T) {
	for _, lang := range []string{"ru", "en"} {
		data, err := json.MarshalIndent(questionExample(lang), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		drafts, errs := questionimport.Parse(data)
		if len(errs) != 0 || len(drafts) != 2 {
			t.Errorf("%s: %d questions, errors %v; want 2 valid questions", lang, len(drafts), errs)
		}
	}
	if questionExample("ru")[0].Text == questionExample("en")[0].Text {
		t.Error("the Russian example is not in Russian")
	}
}

func TestQuestionExampleRoute(t *testing.T) {
	env := newRBACEnv(t)
	for role, want := range map[string]int{"teacher": http.StatusOK, "admin": http.StatusOK, "studentA": http.StatusForbidden} {
		rec := env.do(role, http.MethodGet, "/teacher/questions-example.json", nil)
		if rec.Code != want {
			t.Fatalf("%s: status %d, want %d", role, rec.Code, want)
		}
		if want != http.StatusOK {
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		if cd := rec.Header().Get("Content-Disposition"); cd != "attachment; filename=questions-example.json" {
			t.Errorf("Content-Disposition = %q", cd)
		}
		if _, errs := questionimport.Parse(rec.Body.Bytes()); len(errs) != 0 {
			t.Errorf("served example is invalid: %v", errs)
		}
	}
}
