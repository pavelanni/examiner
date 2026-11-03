package model

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var whitespaceRegex = regexp.MustCompile(`\s+`)

// NormalizeQuestionText prepares question text for duplicate detection (FR1-03):
// - Unicode NFC normalization
// - Lowercase conversion
// - Replacing 'ё' with 'е'
// - Collapsing internal whitespace sequences to a single space
// - Trimming leading and trailing whitespace
// - Stripping trailing punctuation (e.g., '?', '!', '.', etc.)
func NormalizeQuestionText(text string) string {
	if text == "" {
		return ""
	}

	normalized := norm.NFC.String(text)
	normalized = strings.ToLower(normalized)
	normalized = strings.ReplaceAll(normalized, "ё", "е")
	normalized = whitespaceRegex.ReplaceAllString(normalized, " ")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.TrimRightFunc(normalized, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})

	return normalized
}
