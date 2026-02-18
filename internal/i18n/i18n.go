package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

var jsonUnmarshal = json.Unmarshal

//go:embed locales/*.json
var localeFS embed.FS

type ctxKey struct{}

var bundle *i18n.Bundle

// Init loads the translation bundle for the given language tag.
func Init(lang string) error {
	tag, err := language.Parse(lang)
	if err != nil {
		return fmt.Errorf("parse language %q: %w", lang, err)
	}

	bundle = i18n.NewBundle(tag)
	bundle.RegisterUnmarshalFunc("json", jsonUnmarshal)

	// Load all locale files from embedded FS.
	entries, err := localeFS.ReadDir("locales")
	if err != nil {
		return fmt.Errorf("read locales dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := localeFS.ReadFile("locales/" + e.Name())
		if err != nil {
			return fmt.Errorf("read locale file %s: %w", e.Name(), err)
		}
		bundle.MustParseMessageFileBytes(data, e.Name())
		slog.Info("loaded locale file", "file", e.Name())
	}

	return nil
}

// NewLocalizer creates a localizer for the given language.
func NewLocalizer(lang string) *i18n.Localizer {
	return i18n.NewLocalizer(bundle, lang)
}

// WithLocalizer stores a localizer in the context.
func WithLocalizer(ctx context.Context, loc *i18n.Localizer) context.Context {
	return context.WithValue(ctx, ctxKey{}, loc)
}

// localizerFromCtx retrieves the localizer from context.
func localizerFromCtx(ctx context.Context) *i18n.Localizer {
	if loc, ok := ctx.Value(ctxKey{}).(*i18n.Localizer); ok {
		return loc
	}
	// Fallback: return English localizer.
	return i18n.NewLocalizer(bundle, "en")
}

// T translates a message by ID.
func T(ctx context.Context, msgID string) string {
	loc := localizerFromCtx(ctx)
	s, err := loc.Localize(&i18n.LocalizeConfig{MessageID: msgID})
	if err != nil {
		slog.Warn("missing translation", "id", msgID, "error", err)
		return msgID
	}
	return s
}

// Td translates a message by ID with template data.
func Td(ctx context.Context, msgID string, data map[string]any) string {
	loc := localizerFromCtx(ctx)
	s, err := loc.Localize(&i18n.LocalizeConfig{
		MessageID:    msgID,
		TemplateData: data,
	})
	if err != nil {
		slog.Warn("missing translation", "id", msgID, "error", err)
		return msgID
	}
	return s
}

// Tp translates a pluralized message by ID.
func Tp(ctx context.Context, msgID string, count int) string {
	loc := localizerFromCtx(ctx)
	s, err := loc.Localize(&i18n.LocalizeConfig{
		MessageID:    msgID,
		PluralCount:  count,
		TemplateData: map[string]any{"Count": count},
	})
	if err != nil {
		slog.Warn("missing translation", "id", msgID, "error", err)
		return msgID
	}
	return s
}
