package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pavelanni/examiner/internal/access"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/questionimport"
	"github.com/pavelanni/examiner/internal/store"
)

// backfillQuestionOwners gives an owner to every question that has none.
// A question listed in exactly one teacher's file in dir
// (teacher_<username>_<name>.json) goes to that teacher; all others go to
// the first admin. Questions that already have an owner are not touched,
// so running it on every start is safe. A missing dir is not an error.
func backfillQuestionOwners(db *store.Store, dir string) error {
	users, err := db.ListUsers()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", dir, err)
	}

	claims := map[int64][]int64{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		owner := access.QuestionFileOwner(users, name)
		if owner == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			slog.Warn("cannot read questions file for owner backfill", "file", name, "error", err)
			continue
		}
		drafts, _ := questionimport.Decode(data)
		for _, d := range drafts {
			ids, err := db.FindUnownedQuestionIDs(d.Topic, model.NormalizeQuestionText(d.Text))
			if err != nil {
				return err
			}
			for _, id := range ids {
				claims[id] = append(claims[id], owner.ID)
			}
		}
	}
	return db.ClaimQuestionOwners(claims)
}
