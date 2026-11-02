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
		msg := i18n.Td(ctx, e.Code, withOwner(ctx, e.Params))
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

// LocalizeDuplicate renders the question about one duplicate in the
// context's language.
func LocalizeDuplicate(ctx context.Context, d Duplicate) string {
	return i18n.Td(ctx, d.Code, withOwner(ctx, d.Params))
}

// withOwner names the owner of a question whose owner has no display name
// (legacy rows, assigned to the administrator). It never changes params.
func withOwner(ctx context.Context, params map[string]any) map[string]any {
	if owner, ok := params["Owner"]; !ok || owner != "" {
		return params
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	out["Owner"] = i18n.T(ctx, "QImportOwnerAdmin")
	return out
}
