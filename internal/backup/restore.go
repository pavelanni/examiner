package backup

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RestoreResult reports what Restore did.
type RestoreResult struct {
	// Previous is where the replaced database was moved, with its -wal and
	// -shm files next to it under the same name; "" if there was none.
	Previous string
}

// Restore replaces the database at dbPath with the backup at backupPath:
// a .db.gz backup, or a plain .db file such as a database moved aside by an
// earlier restore (so a restore can be rolled back). A plain file is copied
// with VACUUM INTO, which includes changes still in its -wal file. The copy
// is made next to dbPath and checked with integrity_check first; on any
// error dbPath is untouched.
// The current database and its -wal and -shm files are moved aside, not
// deleted, so the restore can be undone.
//
// The server must be stopped, and Restore must run as the user the server
// runs as, so the restored file has the owner the server expects.
func Restore(ctx context.Context, backupPath, dbPath string, now time.Time) (RestoreResult, error) {
	dir := filepath.Dir(dbPath)
	tmp, err := os.CreateTemp(dir, ".restore-*.db")
	if err != nil {
		return RestoreResult{}, err
	}
	unpacked := tmp.Name()
	placed := false
	defer func() {
		if !placed {
			removeDB(unpacked)
		}
	}()
	if err := copyBackup(ctx, backupPath, tmp); err != nil {
		return RestoreResult{}, fmt.Errorf("read %s: %w", filepath.Base(backupPath), err)
	}
	if err := Check(ctx, unpacked); err != nil {
		return RestoreResult{}, fmt.Errorf("backup %s: %w", filepath.Base(backupPath), err)
	}
	// Reading the copy may leave empty -wal and -shm files; they belong to
	// the temporary name, not to dbPath.
	for _, side := range []string{"-wal", "-shm"} {
		_ = os.Remove(unpacked + side)
	}

	var res RestoreResult
	if fi, err := os.Stat(dbPath); err == nil {
		res.Previous = dbPath + ".pre-restore-" + now.UTC().Format(timeLayout)
		for _, side := range []string{"", "-wal", "-shm"} {
			err := os.Rename(dbPath+side, res.Previous+side)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return res, fmt.Errorf("move the current database aside: %w", err)
			}
		}
		// Keep the permissions the server's file had.
		_ = os.Chmod(unpacked, fi.Mode().Perm())
	} else if !errors.Is(err, os.ErrNotExist) {
		return RestoreResult{}, err
	}

	if err := os.Rename(unpacked, dbPath); err != nil {
		return res, fmt.Errorf("put the backup in place: %w", err)
	}
	placed = true
	return res, nil
}

// copyBackup writes the database in path to dst, which it closes: a .gz
// backup is decompressed, a plain database is snapshotted with VACUUM INTO.
func copyBackup(ctx context.Context, path string, dst *os.File) error {
	if !strings.HasSuffix(path, ".gz") {
		// VACUUM INTO needs a file that does not exist yet.
		_ = dst.Close()
		if err := os.Remove(dst.Name()); err != nil {
			return err
		}
		db, err := OpenReadOnly(path)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		return DB{DB: db}.SnapshotTo(ctx, dst.Name())
	}
	f, err := os.Open(path)
	if err != nil {
		_ = dst.Close()
		return err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		_ = dst.Close()
		return err
	}
	defer func() { _ = zr.Close() }()
	if _, err := io.Copy(dst, zr); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}
