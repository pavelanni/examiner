package questionimport

import (
	"fmt"

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
	// DeleteUnused deletes the active question with this topic and exact
	// text unless an exam session references it.
	DeleteUnused(courseID int64, topic, text string) error
	SetImportedFileHash(name, hash string) error
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
	// Those whose identity is not in the new file are deleted unless an
	// exam session uses them.
	Old  []Draft
	File *FileRecord // recorded in imported_files inside the transaction
	// Editable reports whether ModeUpdateExisting may change an existing
	// question; a question it rejects is skipped with a warning. Nil
	// allows every question.
	Editable func(existing model.Question) bool
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
	var res Result
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
		if opts.Mode != ModeUpdateExisting || (opts.Editable != nil && !opts.Editable(*existing)) {
			res.skip(d.Number, "QImportWarnDupInDB", map[string]any{"ID": existing.ID, "Topic": d.Topic})
			continue
		}
		q.ID = existing.ID
		if err := tx.Update(q); err != nil {
			return Result{}, fmt.Errorf("question %d: update #%d: %w", d.Number, existing.ID, err)
		}
		res.Updated++
		res.Saved = append(res.Saved, d.Number)
	}

	for _, d := range opts.Old {
		if _, kept := seen[identity{d.Topic, model.NormalizeQuestionText(d.Text)}]; kept {
			continue
		}
		if err := tx.DeleteUnused(opts.CourseID, d.Topic, d.Text); err != nil {
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
