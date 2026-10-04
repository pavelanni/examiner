package sqlitedb_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pavelanni/examiner/internal/sqlitedb"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqlitedb.DSN(filepath.Join(t.TempDir(), "t.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestPragmasOnEveryConnection holds several pool connections open at once
// and checks that each one got the pragmas.
func TestPragmasOnEveryConnection(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	const n = 4
	conns := make([]*sql.Conn, 0, n)
	for range n {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		t.Cleanup(func() { _ = c.Close() })
	}

	want := map[string]string{
		"journal_mode": "wal",
		"busy_timeout": "5000",
		"foreign_keys": "1",
		"synchronous":  "1", // NORMAL
	}
	for i, c := range conns {
		for pragma, exp := range want {
			var got string
			if err := c.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
				t.Fatalf("conn %d PRAGMA %s: %v", i, pragma, err)
			}
			if got != exp {
				t.Errorf("conn %d PRAGMA %s = %q, want %q", i, pragma, got, exp)
			}
		}
	}
}

// TestConcurrentWrites reproduces the "database is locked" failure that
// 50 simultaneous students would trigger with busy_timeout = 0.
func TestConcurrentWrites(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE m (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT)`); err != nil {
		t.Fatal(err)
	}

	const writers = 50
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Go(func() {
			if _, err := db.Exec(`INSERT INTO m (body) VALUES ('x')`); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent insert: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM m`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != writers {
		t.Errorf("rows = %d, want %d", count, writers)
	}
}

func TestCheckForeignKeys(t *testing.T) {
	db := openTestDB(t)
	// Insert a dangling child with enforcement off on a dedicated
	// connection, as an old database would contain.
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, q := range []string{
		`CREATE TABLE p (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES p(id))`,
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO c (pid) VALUES (42)`,
	} {
		if _, err := c.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	vs, err := sqlitedb.CheckForeignKeys(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Table != "c" || vs[0].Parent != "p" {
		t.Errorf("violations = %+v, want one for c -> p", vs)
	}
}

func TestDSNPreservesExistingQuery(t *testing.T) {
	if got := sqlitedb.DSN("exam.db"); !strings.HasPrefix(got, "exam.db?_pragma=") {
		t.Errorf("plain path: got %q", got)
	}
	got := sqlitedb.DSN("file:exam.db?cache=shared")
	if !strings.HasPrefix(got, "file:exam.db?cache=shared&_pragma=") || strings.Count(got, "?") != 1 {
		t.Errorf("URI path: got %q", got)
	}
}
