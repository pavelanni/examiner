package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// saveTeacherQuestionFile stages the upload on the same filesystem and keeps
// the previous files until the database commit succeeds. This compensates for
// returned errors; a process crash between publication and commit needs recovery.
func saveTeacherQuestionFile(oldPath, outPath string, save func(func([]byte) error) error) error {
	dir, err := os.MkdirTemp(filepath.Dir(outPath), ".upload-")
	if err != nil {
		return err
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			if err := os.RemoveAll(dir); err != nil {
				slog.Warn("failed to clean upload staging directory", "error", err)
			}
		}
	}()

	staged := filepath.Join(dir, "new")
	previous := filepath.Join(dir, "previous")
	retired := filepath.Join(dir, "retired")
	var backedUp, published, movedOld bool

	err = save(func(data []byte) error {
		if err := os.WriteFile(staged, data, 0644); err != nil {
			return err
		}
		info, err := os.Lstat(outPath)
		switch {
		case err == nil:
			if !info.Mode().IsRegular() {
				return fmt.Errorf("destination is not a regular file")
			}
			if err := os.Link(outPath, previous); err != nil {
				return err
			}
			backedUp = true
		case !errors.Is(err, os.ErrNotExist):
			return err
		}

		if err := os.Rename(staged, outPath); err != nil {
			return err
		}
		published = true
		if oldPath != "" && oldPath != outPath {
			if err := os.Rename(oldPath, retired); err != nil {
				return err
			}
			movedOld = true
		}
		return nil
	})
	if err == nil {
		return nil
	}

	var restoreErr error
	if movedOld {
		restoreErr = os.Rename(retired, oldPath)
	}
	if published {
		if backedUp {
			restoreErr = errors.Join(restoreErr, os.Rename(previous, outPath))
		} else {
			restoreErr = errors.Join(restoreErr, os.Remove(outPath))
		}
	}
	if restoreErr != nil {
		keepBackup = true
		return errors.Join(err, fmt.Errorf("restore question files (backup at %s): %w", dir, restoreErr))
	}
	return err
}
