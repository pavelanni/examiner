// Package sqlitedb builds the connection string shared by every SQLite
// store in the project.
package sqlitedb

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
)

// DSN returns a modernc.org/sqlite connection string for path.
//
// The modernc driver only understands _pragma, _time_format and _txlock;
// mattn-style parameters such as _journal_mode are silently ignored. The
// pragmas below run on every new pooled connection.
//
//   - busy_timeout: a second writer waits instead of failing at once with
//     SQLITE_BUSY ("database is locked").
//   - journal_mode(WAL): readers and a writer don't block each other.
//   - synchronous(NORMAL): the usual pairing with WAL.
//   - foreign_keys(1): enforce the FOREIGN KEY clauses in the schema.
//   - _txlock=immediate: transactions take the write lock at BEGIN, so
//     busy_timeout applies instead of a mid-transaction deadlock error.
func DSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	return path + "?" + q.Encode()
}

// ForeignKeyViolation is one row reported by PRAGMA foreign_key_check.
type ForeignKeyViolation struct {
	Table  string
	RowID  sql.NullInt64 // NULL for WITHOUT ROWID tables
	Parent string
}

// CheckForeignKeys reports rows that reference a missing parent row.
// Databases created before foreign_keys was actually enabled may hold
// such rows, and writes that touch them will start failing.
func CheckForeignKeys(db *sql.DB) ([]ForeignKeyViolation, error) {
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return nil, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ForeignKeyViolation
	for rows.Next() {
		var v ForeignKeyViolation
		var fkid int
		if err := rows.Scan(&v.Table, &v.RowID, &v.Parent, &fkid); err != nil {
			return nil, fmt.Errorf("scan foreign_key_check: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// WarnForeignKeys logs every violation found by CheckForeignKeys. It
// warns instead of failing so that an existing database still starts.
func WarnForeignKeys(db *sql.DB) {
	vs, err := CheckForeignKeys(db)
	if err != nil {
		slog.Error("foreign key check failed", "error", err)
		return
	}
	for _, v := range vs {
		slog.Warn("dangling foreign key", "table", v.Table, "rowid", v.RowID.Int64, "parent", v.Parent)
	}
}
