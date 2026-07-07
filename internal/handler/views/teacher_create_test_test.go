package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
)

func TestTeacherCreateTestPageInitializesAddQuestionHandler(t *testing.T) {
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("Init(en): %v", err)
	}

	ctx := i18n.WithLocalizer(context.Background(), i18n.NewLocalizer("en"))

	var buf bytes.Buffer
	if err := TeacherCreateTestPage("Teacher", "csrf-token", `{"type":"object"}`).Render(ctx, &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "DOMContentLoaded") {
		t.Fatalf("expected DOMContentLoaded initialization in rendered page, got %q", html)
	}
	if !strings.Contains(html, "addEventListener('click', addQuestion)") {
		t.Fatalf("expected add-question button handler in rendered page, got %q", html)
	}
}
