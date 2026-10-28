package questionimport

import (
	"errors"
	"fmt"

	"github.com/pavelanni/examiner/internal/access"
	"github.com/pavelanni/examiner/internal/model"
)

// Tx is the transaction-scoped store surface Apply needs.
// *store.QuestionTx implements it.
type Tx interface {
	// FindByTopicAndTextNorm returns the active question with this
	// identity (course, topic, normalized text), or nil.
	FindByTopicAndTextNorm(courseID int64, topic, textNorm string) (*model.Question, error)
	Insert(q model.Question) (int64, error)
	Update(q model.Question) error
	// DeleteUnused deletes ownerID's active question with this topic and
	// exact text unless an exam session references it.
	DeleteUnused(courseID, ownerID int64, topic, text string) error
	SetImportedFileHash(name, hash string) error
	UserDisplayName(id int64) (string, error)
}

// Mode says what to do with a question that already exists in the topic.
type Mode int

const (
	// ModeSkipExisting skips it with a warning (admin import, startup).
	ModeSkipExisting Mode = iota
	// ModeUpdateExisting updates it in place (teacher save).
	ModeUpdateExisting
)

// FileRecord is the imported file to record with the questions.
type FileRecord struct {
	Name, Hash string
}

// Options controls Apply.
type Options struct {
	Mode     Mode
	CourseID int64
	// Old holds the questions of the file being replaced (teacher edit).
	// The actor's questions whose identity is not in the new file are
	// deleted unless an exam session uses them.
	Old  []Draft
	File *FileRecord // recorded in imported_files inside the transaction

	Actor     *model.User // who imports; owns inserted questions
	Published bool        // publication state of inserted and updated questions
}

// ConflictError lists questions in the file that already exist and belong
// to someone the actor may not edit. Callers show it like validation errors.
type ConflictError struct {
	Errs []FieldError
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%d question(s) belong to another owner", len(e.Errs))
}

// Result counts what Apply did.
type Result struct {
	Inserted, Updated, Skipped int
	Warnings                   []Warning
	// Saved lists the numbers of the questions inserted or updated, in
	// file order.
	Saved []int
}

// identity is a question's key among active questions of a course.
type identity struct{ topic, norm string }

// Apply writes validated questions through tx. Run it inside one
// transaction: on error the caller rolls back and the Result is discarded.
func Apply(tx Tx, drafts []Draft, opts Options) (Result, error) {
	if opts.Actor == nil {
		return Result{}, errors.New("import without an acting user")
	}
	var res Result
	var conflicts []FieldError
	seen := make(map[identity]int, len(drafts)) // -> question number

	for _, d := range drafts {
		norm := model.NormalizeQuestionText(d.Text)
		key := identity{d.Topic, norm}

		if prev, ok := seen[key]; ok {
			res.skip(d.Number, "QImportWarnDupInFile", map[string]any{"PrevN": prev, "Topic": d.Topic})
			continue
		}
		seen[key] = d.Number

		q := model.Question{
			CourseID:    opts.CourseID,
			Text:        d.Text,
			TextNorm:    norm,
			Difficulty:  d.Difficulty,
			Topic:       d.Topic,
			Rubric:      d.Rubric,
			ModelAnswer: d.ModelAnswer,
			MaxPoints:   d.MaxPoints,
			OwnerID:     opts.Actor.ID,
			Published:   opts.Published,
		}

		existing, err := tx.FindByTopicAndTextNorm(opts.CourseID, d.Topic, norm)
		if err != nil {
			return Result{}, fmt.Errorf("question %d: find duplicate: %w", d.Number, err)
		}
		if existing == nil {
			if _, err := tx.Insert(q); err != nil {
				return Result{}, fmt.Errorf("question %d: insert: %w", d.Number, err)
			}
			res.Inserted++
			res.Saved = append(res.Saved, d.Number)
			continue
		}
		if opts.Mode != ModeUpdateExisting {
			res.skip(d.Number, "QImportWarnDupInDB", map[string]any{"ID": existing.ID, "Topic": d.Topic})
			continue
		}
		if !access.CanEditQuestion(opts.Actor, *existing) {
			owner, err := tx.UserDisplayName(existing.OwnerID)
			if err != nil {
				return Result{}, fmt.Errorf("question %d: owner name: %w", d.Number, err)
			}
			conflicts = append(conflicts, FieldError{Question: d.Number, Code: "QImportErrNotOwner",
				Params: map[string]any{"ID": existing.ID, "Owner": owner, "Topic": d.Topic}})
			continue
		}
		q.ID = existing.ID
		q.OwnerID = existing.OwnerID
		if err := tx.Update(q); err != nil {
			return Result{}, fmt.Errorf("question %d: update #%d: %w", d.Number, existing.ID, err)
		}
		res.Updated++
		res.Saved = append(res.Saved, d.Number)
	}

	if len(conflicts) > 0 {
		return Result{}, &ConflictError{Errs: conflicts}
	}
	for _, d := range opts.Old {
		if _, kept := seen[identity{d.Topic, model.NormalizeQuestionText(d.Text)}]; kept {
			continue
		}
		if err := tx.DeleteUnused(opts.CourseID, opts.Actor.ID, d.Topic, d.Text); err != nil {
			return Result{}, fmt.Errorf("delete replaced question %d: %w", d.Number, err)
		}
	}
	if opts.File != nil {
		if err := tx.SetImportedFileHash(opts.File.Name, opts.File.Hash); err != nil {
			return Result{}, fmt.Errorf("record imported file: %w", err)
		}
	}
	return res, nil
}

func (r *Result) skip(n int, code string, params map[string]any) {
	params["N"] = n
	r.Skipped++
	r.Warnings = append(r.Warnings, Warning{Question: n, Code: code, Params: params})
}
