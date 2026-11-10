package handler

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveTeacherQuestionFileRestoresFilesAfterCommitFailure(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.json")
	outPath := filepath.Join(dir, "target.json")
	files := map[string]string{oldPath: "old", outPath: "previous target"}
	for path, data := range files {
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	commitErr := errors.New("test commit failure")
	err := saveTeacherQuestionFile(oldPath, outPath, []byte("new"), func(publish func() error) error {
		if err := publish(); err != nil {
			return err
		}
		return commitErr
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("got %v, want commit failure", err)
	}
	for path, want := range files {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s: got %q, %v, want %q", path, data, err, want)
		}
	}
}
