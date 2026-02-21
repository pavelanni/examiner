package i18n

import (
	"context"
	"testing"
)

func initLang(t *testing.T, lang string) context.Context {
	t.Helper()
	if err := Init(lang); err != nil {
		t.Fatalf("Init(%q): %v", lang, err)
	}
	loc := NewLocalizer(lang)
	return WithLocalizer(context.Background(), loc)
}

func TestTranslateEnglish(t *testing.T) {
	ctx := initLang(t, "en")

	got := T(ctx, "AppTitle")
	if got != "Examiner" {
		t.Errorf("T(AppTitle) = %q, want 'Examiner'", got)
	}

	got = T(ctx, "StartExam")
	if got != "Start Exam" {
		t.Errorf("T(StartExam) = %q, want 'Start Exam'", got)
	}
}

func TestTranslateRussian(t *testing.T) {
	ctx := initLang(t, "ru")

	got := T(ctx, "AppTitle")
	if got != "Экзаменатор" {
		t.Errorf("T(AppTitle) = %q, want 'Экзаменатор'", got)
	}

	got = T(ctx, "StartExam")
	if got != "Начать экзамен" {
		t.Errorf("T(StartExam) = %q, want 'Начать экзамен'", got)
	}
}

func TestPluralTranslation(t *testing.T) {
	ctx := initLang(t, "en")

	got1 := Tp(ctx, "QuestionsAvailable", 1)
	if got1 != "1 question available." {
		t.Errorf("Tp(QuestionsAvailable, 1) = %q, want '1 question available.'", got1)
	}

	got5 := Tp(ctx, "QuestionsAvailable", 5)
	if got5 != "5 questions available." {
		t.Errorf("Tp(QuestionsAvailable, 5) = %q, want '5 questions available.'", got5)
	}
}

func TestTemplateDataTranslation(t *testing.T) {
	ctx := initLang(t, "en")

	got := Td(ctx, "SessionN", map[string]any{"ID": 42})
	if got != "Session #42" {
		t.Errorf("Td(SessionN, ID=42) = %q, want 'Session #42'", got)
	}
}

func TestMissingKey(t *testing.T) {
	ctx := initLang(t, "en")

	got := T(ctx, "NonExistentKey")
	if got != "NonExistentKey" {
		t.Errorf("T(NonExistentKey) = %q, want 'NonExistentKey'", got)
	}
}
