package views

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
)

// langCtx returns a context that translates into lang.
func langCtx(t *testing.T, lang string) context.Context {
	t.Helper()
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return i18n.WithLocalizer(context.Background(), i18n.NewLocalizer(lang))
}

func TestSessionStatusLabelAll(t *testing.T) {
	ctx := langCtx(t, "ru")
	for _, s := range []model.SessionStatus{
		model.StatusInProgress, model.StatusSubmitted, model.StatusGrading, model.StatusGradingFailed,
		model.StatusGraded, model.StatusReviewed, model.StatusAnnulled,
	} {
		if got := sessionStatusLabel(ctx, s); got == string(s) || got == "" {
			t.Errorf("status %q is not translated: %q", s, got)
		}
	}
	for s, want := range map[model.SessionStatus]string{
		model.StatusGraded:     "Ожидает проверки",
		model.StatusInProgress: "Идёт экзамен",
	} {
		if got := sessionStatusLabel(ctx, s); got != want {
			t.Errorf("sessionStatusLabel(%q) = %q, want %q", s, got, want)
		}
	}
	if got := sessionStatusLabel(ctx, "something_new"); got != "something_new" {
		t.Errorf("unknown status = %q, want the raw value", got)
	}
}

func TestRoleLabel(t *testing.T) {
	for lang, want := range map[string][3]string{
		"ru": {"Студент", "Преподаватель", "Администратор"},
		"en": {"Student", "Teacher", "Administrator"},
	} {
		ctx := langCtx(t, lang)
		for i, r := range []model.UserRole{model.UserRoleStudent, model.UserRoleTeacher, model.UserRoleAdmin} {
			if got := roleLabel(ctx, r); got != want[i] {
				t.Errorf("%s: roleLabel(%q) = %q, want %q", lang, r, got, want[i])
			}
		}
	}
}

func TestDifficultyListLabel(t *testing.T) {
	ctx := langCtx(t, "ru")
	if got := difficultyListLabel(ctx, "easy, hard"); got != "Лёгкий, Сложный" {
		t.Errorf("difficultyListLabel = %q", got)
	}
}

// renderRU renders c in Russian with user (may be nil) logged in.
func renderRU(t *testing.T, user *model.User, c interface {
	Render(context.Context, io.Writer) error
}) string {
	t.Helper()
	ctx := model.ContextWithUser(langCtx(t, "ru"), user)
	var buf bytes.Buffer
	if err := c.Render(ctx, &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestLayoutShowsTranslatedRole(t *testing.T) {
	html := renderRU(t, &model.User{DisplayName: "Анна", Role: model.UserRoleTeacher}, Layout("x"))
	if !strings.Contains(html, "(Преподаватель)") || strings.Contains(html, "(teacher)") {
		t.Errorf("header role not translated")
	}
}

func TestEvaluatorFollowupTranslated(t *testing.T) {
	html := renderRU(t, nil, evaluatorMessage(model.Message{Content: "ok", Followup: "Почему?"}))
	if !strings.Contains(html, "Уточняющий вопрос:") || strings.Contains(html, "Follow-up question") {
		t.Errorf("follow-up label not translated: %s", html)
	}
}

func TestDifficultyLabel(t *testing.T) {
	ctx := langCtx(t, "ru")
	want := []string{"Лёгкий", "Средний", "Сложный"}
	for i, d := range model.Difficulties {
		if got := DifficultyLabel(ctx, d); got != want[i] {
			t.Errorf("DifficultyLabel(%q) = %q, want %q", d, got, want[i])
		}
	}
}

func TestAdminQuestionsPageHasFormatHelp(t *testing.T) {
	html := renderRU(t, &model.User{Role: model.UserRoleAdmin}, AdminQuestionsPage("", false, nil, nil, nil, `[{"model_answer": "x"}]`))
	// Long example lines wrap instead of being cut off at the page edge.
	for _, want := range []string{"Формат файла", "/teacher/questions-example.json", "Скачать пример", "model_answer", "max_points", "white-space: pre-wrap"} {
		if !strings.Contains(html, want) {
			t.Errorf("format help lacks %q", want)
		}
	}
}
