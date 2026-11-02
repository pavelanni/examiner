package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

func renderMarkdown(t *testing.T, src string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := markdown(src).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestMarkdownFormatsLLMText(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"bold", "a **key** point", "<strong>key</strong>"},
		{"follow-up marker", "Good." + model.FollowupMarker + "Why?", "<strong>Follow-up question:</strong> Why?"},
		{"list", "- one\n- two", "<li>one</li>"},
		{"inline code", "use `F = ma`", "<code>F = ma</code>"},
		{"line break kept", "first\nsecond", "first<br"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderMarkdown(t, tt.src); !strings.Contains(got, tt.want) {
				t.Errorf("render(%q) = %q, want it to contain %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestMarkdownCannotInjectMarkup(t *testing.T) {
	tests := []struct {
		name, src string
		banned    []string
	}{
		{"script tag", "hi <script>alert(1)</script>", []string{"<script"}},
		{"inline handler", `<img src=x onerror="alert(1)">`, []string{"<img", "onerror"}},
		{"javascript link", "[click](javascript:alert(1))", []string{"javascript:"}},
		{"data link", "[click](data:text/html;base64,PHNjcmlwdD4=)", []string{"data:text/html"}},
		{"iframe", "<iframe src=//evil></iframe>", []string{"<iframe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderMarkdown(t, tt.src)
			for _, b := range tt.banned {
				if strings.Contains(strings.ToLower(got), b) {
					t.Errorf("render(%q) = %q, contains %q", tt.src, got, b)
				}
			}
		})
	}
}
