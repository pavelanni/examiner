package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
	"golang.org/x/text/language"
)

// questionExample returns a two-question file in the UI language, for the
// format help and the example download. It must stay valid for
// questionimport.Parse (see the test).
func questionExample(lang string) []model.QuestionImport {
	// Compare the base language: --lang may be "ru-RU", which the UI
	// localizer matches to Russian too.
	if base, _ := language.Make(lang).Base(); base.String() == "ru" {
		return []model.QuestionImport{
			{
				Text:        "Сформулируйте второй закон Ньютона.",
				Difficulty:  model.DifficultyEasy,
				Topic:       "Законы Ньютона",
				Rubric:      "2 балла: ускорение тела пропорционально равнодействующей силе. 2 балла: обратно пропорционально массе. 1 балл: формула F = ma.",
				ModelAnswer: "Ускорение тела прямо пропорционально равнодействующей всех приложенных к нему сил и обратно пропорционально его массе: F = ma.",
				MaxPoints:   5,
			},
			{
				Text:        "Брусок массой 2 кг тянут по горизонтальному столу с силой 10 Н. Коэффициент трения 0,2. Найдите ускорение бруска.",
				Difficulty:  model.DifficultyMedium,
				Topic:       "Законы Ньютона",
				Rubric:      "2 балла: сила трения 4 Н (μmg при g = 10 м/с²). 2 балла: равнодействующая 6 Н. 2 балла: ускорение 3 м/с².",
				ModelAnswer: "Сила трения μmg = 0,2 · 2 · 10 = 4 Н. Равнодействующая 10 − 4 = 6 Н. Ускорение a = F/m = 6 / 2 = 3 м/с².",
				MaxPoints:   6,
			},
		}
	}
	return []model.QuestionImport{
		{
			Text:        "State Newton's second law.",
			Difficulty:  model.DifficultyEasy,
			Topic:       "Newton's laws",
			Rubric:      "2 points: acceleration is proportional to the net force. 2 points: inversely proportional to mass. 1 point: the formula F = ma.",
			ModelAnswer: "The acceleration of a body is directly proportional to the net force acting on it and inversely proportional to its mass: F = ma.",
			MaxPoints:   5,
		},
		{
			Text:        "A 2 kg block is pulled along a horizontal table with a force of 10 N. The friction coefficient is 0.2. Find the block's acceleration.",
			Difficulty:  model.DifficultyMedium,
			Topic:       "Newton's laws",
			Rubric:      "2 points: friction force 4 N (μmg with g = 10 m/s²). 2 points: net force 6 N. 2 points: acceleration 3 m/s².",
			ModelAnswer: "Friction is μmg = 0.2 · 2 · 10 = 4 N. The net force is 10 − 4 = 6 N. The acceleration is a = F/m = 6 / 2 = 3 m/s².",
			MaxPoints:   6,
		},
	}
}

// questionExampleJSON is questionExample as an indented JSON file.
func questionExampleJSON(lang string) []byte {
	data, err := json.MarshalIndent(questionExample(lang), "", "  ")
	if err != nil {
		// A fixed value of plain types always marshals.
		slog.Error("encode question example", "error", err)
		return []byte("[]")
	}
	return data
}

// handleQuestionExample serves the example question file for download.
func (h *Handler) handleQuestionExample(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=questions-example.json")
	_, _ = w.Write(questionExampleJSON(i18n.Lang(r.Context())))
}
