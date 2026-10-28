package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appI18n "github.com/pavelanni/examiner/internal/i18n"
	"github.com/pavelanni/examiner/internal/store"
)

func loadEnv(t *testing.T) (context.Context, *store.Store) {
	t.Helper()
	if err := appI18n.Init("en"); err != nil {
		t.Fatalf("i18n.Init: %v", err)
	}
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return appI18n.WithLocalizer(context.Background(), appI18n.NewLocalizer("en")), s
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func q(text, rubric, points string) string {
	return `{"text":"` + text + `","difficulty":"easy","topic":"t","rubric":"` + rubric + `","model_answer":"a","max_points":` + points + `}`
}

func count(t *testing.T, s *store.Store) int {
	t.Helper()
	n, err := s.QuestionCount()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLoadQuestionsInvalidFileFails(t *testing.T) {
	ctx, s := loadEnv(t)
	path := writeFile(t, "q.json", `[`+q("A", "", "5")+`,`+q("B", "r", "5")+`,`+q("C", "r", "0")+`]`)
	err := loadQuestions(ctx, s, []string{path}, 3, 0)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	for _, want := range []string{"Question 1, field rubric", "Question 3, field max_points"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if n := count(t, s); n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
}

func TestLoadQuestionsInvalidEvenIfImported(t *testing.T) {
	ctx, s := loadEnv(t)
	invalid := `[` + q("A", "", "5") + `]`
	path := writeFile(t, "q.json", invalid)
	if err := s.SetImportedFileHash(path, sha256sum([]byte(invalid))); err != nil {
		t.Fatal(err)
	}
	if err := loadQuestions(ctx, s, []string{path}, 3, 0); err == nil {
		t.Fatal("an invalid file must fail even when its hash is already recorded")
	}
}

func TestLoadQuestionsWrapperFormat(t *testing.T) {
	ctx, s := loadEnv(t)
	content := `{"questions":[` + q("A", "r", "5") + `,` + q("B", "r", "5") + `]}`
	path := writeFile(t, "q.json", content)
	if err := loadQuestions(ctx, s, []string{path}, 3, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	if h, _ := s.GetImportedFileHash(path); h != sha256sum([]byte(content)) {
		t.Errorf("hash = %q, not recorded", h)
	}
}

func TestLoadQuestionsUnchangedSkips(t *testing.T) {
	ctx, s := loadEnv(t)
	path := writeFile(t, "q.json", `[`+q("A", "r", "5")+`]`)
	for i := 0; i < 2; i++ {
		if err := loadQuestions(ctx, s, []string{path}, 3, 0); err != nil {
			t.Fatalf("load %d: %v", i+1, err)
		}
	}
	if n := count(t, s); n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}
