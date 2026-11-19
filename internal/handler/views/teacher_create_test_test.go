package views

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/model"
)

func TestTeacherCreateTestPageInitializesAddQuestionHandler(t *testing.T) {
	if err := i18n.Init("en"); err != nil {
		t.Fatalf("Init(en): %v", err)
	}

	ctx := i18n.WithLocalizer(context.Background(), i18n.NewLocalizer("en"))

	var buf bytes.Buffer
	if err := TeacherCreateTestPage("Teacher", "csrf-token", TeacherBuilder{Published: true}).Render(ctx, &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "DOMContentLoaded") {
		t.Fatalf("expected DOMContentLoaded initialization in rendered page, got %q", html)
	}

	// Updated to match the anonymous arrow function wrapper: () => addQuestion()
	if !strings.Contains(html, "addEventListener('click', () => addQuestion())") {
		t.Fatalf("expected add-question button handler in rendered page, got %q", html)
	}
}

func renderBuilderRU(t *testing.T, b TeacherBuilder) string {
	t.Helper()
	return renderRU(t, &model.User{Role: model.UserRoleTeacher, DisplayName: "T"}, TeacherCreateTestPage("T", "csrf", b))
}

func TestBuilderTemplateRendered(t *testing.T) {
	html := renderBuilderRU(t, TeacherBuilder{Published: true})
	start := strings.Index(html, `<template id="question-template"`)
	end := strings.Index(html, "</template>")
	if start < 0 || end < start {
		t.Fatal("no question template")
	}
	block := html[start:end]
	for _, want := range []string{
		"Текст вопроса", "Тема", "Сложность", "Баллы", "Критерии оценки", "Эталонный ответ", "Удалить вопрос",
		`<option value="easy">Лёгкий</option>`, `<option value="medium">Средний</option>`, `<option value="hard">Сложный</option>`,
		`name="q_text___IDX__"`, `name="q_topic___IDX__"`, `name="q_diff___IDX__"`, `name="q_points___IDX__"`,
		`name="q_rubric___IDX__"`, `name="q_answer___IDX__"`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("question template lacks %q", want)
		}
	}
	for _, bad := range []string{"Question text", "Difficulty", ">easy<", "Remove", "Max points", "Model answer"} {
		if strings.Contains(html, bad) {
			t.Errorf("page still has English %q", bad)
		}
	}
}

func TestBuilderModes(t *testing.T) {
	html := renderBuilderRU(t, TeacherBuilder{Published: true})
	if !strings.Contains(html, "Новый набор вопросов") || strings.Count(html, `role="tab"`) != 2 {
		t.Error("new set: title or the two mode tabs missing")
	}
	if !regexp.MustCompile(`<input[^>]*name="questions_file"[^>]*disabled`).MatchString(html) {
		t.Error("new set: the file input must start disabled (manual mode)")
	}
	if !strings.Contains(html, "Формат файла") {
		t.Error("new set: the file tab has no format help")
	}
	edit := renderBuilderRU(t, TeacherBuilder{Filename: "teacher_teacher1_mid.json", SetName: "mid"})
	if strings.Contains(edit, `role="tab"`) || strings.Contains(edit, `name="questions_file"`) {
		t.Error("edit page: no mode tabs and no file input expected")
	}
}

func TestBuilderEditShowsSetName(t *testing.T) {
	html := renderBuilderRU(t, TeacherBuilder{Filename: "teacher_teacher1_mid.json", SetName: "mid", DefaultTopic: "Оптика"})
	if !regexp.MustCompile(`<input[^>]*name="custom_filename"[^>]*value="mid"`).MatchString(html) {
		t.Error("the name field does not show the set name")
	}
	if !strings.Contains(html, "Набор «mid»") || strings.Contains(html, "teacher_teacher1_mid<") {
		t.Error("title should name the set, not the file")
	}
	if !regexp.MustCompile(`<input[^>]*name="default_topic"[^>]*value="Оптика"`).MatchString(html) {
		t.Error("default topic not prefilled")
	}
}

func TestBuilderKeepsDupForm(t *testing.T) {
	html := renderBuilderRU(t, TeacherBuilder{
		Dups:     []DupChoice{{N: 2, Msg: "Вопрос №2 совпадает", CanUpdate: true, UpdateID: 7}},
		DupToken: "abc",
		Kept:     []KeptDecision{{N: 1, Value: "update:5"}},
	})
	for _, want := range []string{`name="dup_token" value="abc"`, `<input type="hidden" name="dup_1" value="update:5"`, `value="update:7"`} {
		if !strings.Contains(html, want) {
			t.Errorf("duplicate form lacks %q", want)
		}
	}
}
