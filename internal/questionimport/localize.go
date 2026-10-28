package questionimport

import (
	"context"

	"github.com/pavelanni/examiner/internal/i18n"
)

// Warning is a non-fatal note about one question, such as a skipped
// duplicate. Params include N, the question number.
type Warning struct {
	Question int
	Code     string // i18n message ID
	Params   map[string]any
}

// Localize renders errors in the context's language. Question-level
// errors are prefixed with the question number and, if set, the field.
func Localize(ctx context.Context, errs []FieldError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		msg := i18n.Td(ctx, e.Code, e.Params)
		switch {
		case e.Question == 0:
			out = append(out, msg)
		case e.Field != "":
			out = append(out, i18n.Td(ctx, "QImportErrAt", map[string]any{"N": e.Question, "Field": e.Field, "Msg": msg}))
		default:
			out = append(out, i18n.Td(ctx, "QImportErrAtQuestion", map[string]any{"N": e.Question, "Msg": msg}))
		}
	}
	return out
}

// LocalizeWarnings renders warnings in the context's language.
func LocalizeWarnings(ctx context.Context, ws []Warning) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, i18n.Td(ctx, w.Code, w.Params))
	}
	return out
}
