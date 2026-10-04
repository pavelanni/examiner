// Package sqlitedb builds the connection string shared by every SQLite
// store in the project.
package sqlitedb

import "net/url"

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
