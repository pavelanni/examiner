package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// TestServeDoesNotRecoverWhenAddressIsBusy preserves grading in an already running server.
func TestServeDoesNotRecoverWhenAddressIsBusy(t *testing.T) {
	root := t.TempDir()
	schema, err := filepath.Abs("../../schema")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, "schema"), os.DirFS(schema)); err != nil {
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
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	cmd := serveCmd()
	cmd.SetContext(t.Context())
	for flag, value := range map[string]string{
		"db": path, "questions": "", "llm-url": llm.URL + "/v1", "llm-model": "mock",
		"addr": busy.Addr().String(),
	} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	var listenErr *net.OpError
	if err := runServe(cmd, nil); !errors.As(err, &listenErr) || listenErr.Op != "listen" {
		t.Fatalf("expected a listen error, got %v", err)
	}
	reopened, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.GetSession(sess.ID)
	if err != nil || got.Status != model.StatusGrading {
		t.Fatalf("failed startup reset the live attempt: %+v, %v", got, err)
	}
}
