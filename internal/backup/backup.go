// Package backup makes, lists, rotates and restores backups of the
// examiner database (ТЗ п. 3.7). A backup is a consistent snapshot taken
// with VACUUM INTO, so it is safe while the server is writing in WAL
// mode, checked with PRAGMA integrity_check and stored gzip-compressed as
// <name>-YYYYmmdd-HHMMSS.db.gz (UTC). Configuration files can be archived
// next to it as <name>-YYYYmmdd-HHMMSS.config.tar.gz.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

const (
	timeLayout   = "20060102-150405"
	dbSuffix     = ".db.gz"
	configSuffix = ".config.tar.gz"
)

// Source is a database that can write a consistent snapshot of itself to
// a new file. *store.Store implements it for the running server.
type Source interface {
	SnapshotTo(ctx context.Context, path string) error
}

// DB is a Source over an open database handle.
type DB struct{ *sql.DB }

// SnapshotTo writes a consistent copy of the database to path.
func (d DB) SnapshotTo(ctx context.Context, path string) error {
	_, err := d.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

// OpenReadOnly opens the database file at path without the right to
// change it, for taking a backup or counting rows while a server may be
// using the file.
func OpenReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	// An SQLite URI takes the path literally except for %HH escapes and the
	// ? and # delimiters, so only those are escaped.
	uri := "file:" + uriPath.Replace(filepath.ToSlash(path)) + "?" + q.Encode()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

var uriPath = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// Options controls Create.
type Options struct {
	Dir     string   // where backups are kept
	Name    string   // file name prefix, e.g. examiner-ru
	Keep    int      // newest backups to keep; 0 keeps all
	Configs []string // configuration files to archive with the database
	Now     func() time.Time
}

// Info describes one backup.
type Info struct {
	Path       string    // the .db.gz file
	ConfigPath string    // the .config.tar.gz file, "" if none
	Time       time.Time // when it was taken (UTC)
	Size       int64     // size of the .db.gz file
}

// Name returns the backup's file name.
func (i Info) Name() string { return filepath.Base(i.Path) }

// Create takes a backup of src into opts.Dir: a snapshot, checked with
// integrity_check, then compressed, and the configuration files if any.
// Files appear under their final names only when complete. Older
// backups beyond opts.Keep are then deleted.
func Create(ctx context.Context, src Source, opts Options) (Info, error) {
	if err := validName(opts.Name); err != nil {
		return Info{}, err
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return Info{}, fmt.Errorf("create backup directory: %w", err)
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	ts := now().UTC().Truncate(time.Second)
	base := filepath.Join(opts.Dir, opts.Name+"-"+ts.Format(timeLayout))
	info := Info{Path: base + dbSuffix, Time: ts}
	if _, err := os.Stat(info.Path); err == nil {
		return Info{}, fmt.Errorf("backup %s already exists", info.Name())
	}

	// The snapshot goes to a temporary file next to the backups: VACUUM
	// INTO refuses an existing file, so only its name is reserved here.
	tmp, err := os.CreateTemp(opts.Dir, ".snapshot-*.db")
	if err != nil {
		return Info{}, err
	}
	snapshot := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(snapshot)
	defer removeDB(snapshot)

	if err := src.SnapshotTo(ctx, snapshot); err != nil {
		return Info{}, fmt.Errorf("snapshot: %w", err)
	}
	if err := Check(ctx, snapshot); err != nil {
		return Info{}, err
	}
	if err := gzipFile(snapshot, info.Path); err != nil {
		return Info{}, fmt.Errorf("compress: %w", err)
	}
	if fi, err := os.Stat(info.Path); err == nil {
		info.Size = fi.Size()
	}

	if len(opts.Configs) > 0 {
		info.ConfigPath = base + configSuffix
		if err := archiveConfigs(opts.Configs, info.ConfigPath); err != nil {
			_ = os.Remove(info.Path)
			return Info{}, fmt.Errorf("archive configuration: %w", err)
		}
	}

	if opts.Keep > 0 {
		if _, err := Rotate(opts.Dir, opts.Name, opts.Keep); err != nil {
			slog.Warn("failed to rotate backups", "dir", opts.Dir, "error", err)
		}
	}
	return info, nil
}

// Check runs PRAGMA integrity_check on the database file at path and
// returns an error unless it reports "ok".
func Check(ctx context.Context, path string) error {
	db, err := OpenReadOnly(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("integrity_check: %w", err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("integrity_check failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

// List returns the backups named name in dir, newest first.
func List(dir, name string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		ts, ok := parseName(e.Name(), name, dbSuffix)
		if !ok || e.IsDir() {
			continue
		}
		info := Info{Path: filepath.Join(dir, e.Name()), Time: ts}
		if fi, err := e.Info(); err == nil {
			info.Size = fi.Size()
		}
		cfg := strings.TrimSuffix(info.Path, dbSuffix) + configSuffix
		if _, err := os.Stat(cfg); err == nil {
			info.ConfigPath = cfg
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b Info) int { return b.Time.Compare(a.Time) })
	return out, nil
}

// Rotate deletes all but the newest keep backups named name in dir,
// with their configuration archives, and returns the deleted paths.
func Rotate(dir, name string, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	all, err := List(dir, name)
	if err != nil {
		return nil, err
	}
	var deleted []string
	for _, old := range all[min(keep, len(all)):] {
		for _, p := range []string{old.Path, old.ConfigPath} {
			if p == "" {
				continue
			}
			if err := os.Remove(p); err != nil {
				return deleted, err
			}
			deleted = append(deleted, p)
		}
	}
	return deleted, nil
}

// parseName returns the time of a backup file name of the form
// <name>-YYYYmmdd-HHMMSS<suffix>.
func parseName(file, name, suffix string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(file, name+"-")
	if !ok {
		return time.Time{}, false
	}
	stamp, ok := strings.CutSuffix(rest, suffix)
	if !ok {
		return time.Time{}, false
	}
	ts, err := time.Parse(timeLayout, stamp)
	return ts, err == nil
}

// validName accepts a prefix that cannot reach outside the backup
// directory or match another instance's backups by accident.
func validName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\:*?"<>|`) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid backup name %q", name)
	}
	return nil
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	return writeAtomic(dst, func(w io.Writer) error {
		zw := gzip.NewWriter(w)
		zw.Name = filepath.Base(strings.TrimSuffix(dst, ".gz"))
		if _, err := io.Copy(zw, in); err != nil {
			return err
		}
		return zw.Close()
	})
}

// archiveConfigs writes the files to a tar.gz under their base names.
// Files that look like secrets (*.env) are skipped: backups may be copied
// off the host, and secrets are kept separately.
func archiveConfigs(paths []string, dst string) error {
	return writeAtomic(dst, func(w io.Writer) error {
		zw := gzip.NewWriter(w)
		tw := tar.NewWriter(zw)
		seen := map[string]bool{}
		for _, p := range paths {
			name := filepath.Base(p)
			if strings.HasSuffix(name, ".env") {
				slog.Warn("not archiving a secrets file", "path", p)
				continue
			}
			if seen[name] {
				return fmt.Errorf("two configuration files named %s", name)
			}
			seen[name] = true
			if err := addFile(tw, p, name); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		return zw.Close()
	})
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	hdr := &tar.Header{Name: name, Mode: int64(fi.Mode().Perm()), Size: fi.Size(), ModTime: fi.ModTime()}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// writeAtomic writes dst through a temporary file in the same directory
// and renames it into place, so dst is either complete or absent. The
// file is readable by its owner only: backups hold password hashes and
// student answers.
func writeAtomic(dst string, write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := write(tmp); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	ok = true
	return nil
}

// removeDB deletes a database file and its WAL side files.
func removeDB(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Remove(p)
	}
}
