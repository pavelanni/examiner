package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pavelanni/examiner/internal/backup"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

// run executes a subcommand and returns what it printed to stdout.
func run(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	runErr := cmd.Execute()
	os.Stdout = stdout
	_ = w.Close()
	var out bytes.Buffer
	_, _ = io.Copy(&out, r)
	return out.String(), runErr
}

func TestBackupStatsRestoreCommands(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "examiner.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"admin", "st1", "st2"} {
		if _, err := s.CreateUser(model.User{Username: name, PasswordHash: "x", Role: model.UserRoleStudent, Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, "examiner-ru.yaml")
	if err := os.WriteFile(cfg, []byte("lang: ru\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := run(t, statsCmd(), "--db", dbPath)
	if err != nil || !strings.Contains(before, "users                    3") {
		t.Fatalf("stats = %q, %v", before, err)
	}

	out, err := run(t, backupCmd(), "--db", dbPath, "--out-dir", filepath.Join(dir, "b"), "--name", "examiner-ru", "--keep", "3", "--config", cfg)
	if err != nil || !strings.Contains(out, "integrity ok") || !strings.Contains(out, "Config: ") {
		t.Fatalf("backup = %q, %v", out, err)
	}
	list, err := backup.List(filepath.Join(dir, "b"), "examiner-ru")
	if err != nil || len(list) != 1 || list[0].ConfigPath == "" {
		t.Fatalf("backups = %+v, %v", list, err)
	}

	// Changes after the backup are undone by the restore.
	if _, err := s.CreateUser(model.User{Username: "late", PasswordHash: "x", Role: model.UserRoleStudent, Active: true}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	out, err = run(t, restoreCmd(), "--db", dbPath, "--backup", list[0].Path)
	if err != nil || !strings.Contains(out, "Previous database kept as") {
		t.Fatalf("restore = %q, %v", out, err)
	}
	after, err := run(t, statsCmd(), "--db", dbPath)
	if err != nil || after != before {
		t.Errorf("stats after restore =\n%s\nwant\n%s (err %v)", after, before, err)
	}
	if js, err := run(t, statsCmd(), "--db", dbPath, "--json"); err != nil || !strings.Contains(js, `"users": 3`) {
		t.Errorf("stats --json = %q, %v", js, err)
	}
}

func TestBackupCommandFails(t *testing.T) {
	if _, err := run(t, backupCmd(), "--db", filepath.Join(t.TempDir(), "missing.db"), "--out-dir", t.TempDir()); err == nil {
		t.Error("missing database: want an error (non-zero exit)")
	}
	if _, err := run(t, restoreCmd(), "--db", filepath.Join(t.TempDir(), "x.db")); err == nil {
		t.Error("restore without --backup: want an error")
	}
}
