package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// TestServeRecoversInterruptedGrading exercises recovery through the real startup path.
func TestServeRecoversInterruptedGrading(t *testing.T) {
	root := t.TempDir()
	schema, err := filepath.Abs("../../schema")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(schema, filepath.Join(root, "schema")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	path := filepath.Join(root, "exam.db")
	s, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := seedAdmin(s, "test-password"); err != nil {
		t.Fatal(err)
	}
	bp, err := s.CreateBlueprint(model.ExamBlueprint{Name: "Exam"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.InsertQuestion(model.Question{CourseID: 1, Text: "Q", Difficulty: "easy", Topic: "t", Published: true})
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := s.StartSession(bp, 7, "t", []int64{q}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSessionStatus(sess.ID, model.StatusGrading); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected LLM request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"mock"}]}`))
	}))
	defer llm.Close()
	cmd := serveCmd()
	cmd.SetContext(t.Context())
	for flag, value := range map[string]string{
		"db": path, "questions": "", "llm-url": llm.URL + "/v1", "llm-model": "mock",
		// A deterministic listen error ends runServe after initialization,
		// without starting a long-lived HTTP server in the test.
		"addr": "invalid-address-without-port",
	} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := runServe(cmd, nil); err == nil || !strings.Contains(err.Error(), "missing port") {
		t.Fatalf("expected the final listen error, got %v", err)
	}
	reopened, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.GetSession(sess.ID)
	if err != nil || got.Status != model.StatusInProgress {
		t.Fatalf("startup left the attempt stuck: %+v, %v", got, err)
	}
}
