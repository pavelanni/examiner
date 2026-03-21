package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pavelanni/examiner/internal/grader/store"
)

func TestNew(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
}

// newTestStore is a helper for creating a test store instance.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}
