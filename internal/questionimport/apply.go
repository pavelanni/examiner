package questionimport

import (
	"fmt"

	"github.com/pavelanni/examiner/internal/model"
)

// Tx is the transaction-scoped store surface Apply needs.
// *store.QuestionTx implements it.
type Tx interface {
	FindByTopicAndTextNorm(topic, textNorm string) (*model.Question, error)
	FindByCourseAndText(courseID int64, text string) (*model.Question, error)
	Insert(q model.Question) (int64, error)
	Update(q model.Question) error
	DeleteUnusedByTexts(courseID int64, oldTexts, keepTexts []string) error
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
	OldTexts []string    // teacher edit: questions of the file being replaced
	File     *FileRecord // recorded in imported_files inside the transaction
}

// Result counts what Apply did.
type Result struct {
	Inserted, Updated, Skipped int
	Warnings                   []Warning
}

// Apply writes validated questions through tx. Run it inside one
// transaction: on error the caller rolls back and the Result is discarded.
func Apply(tx Tx, drafts []Draft, opts Options) (Result, error) {
	var res Result
	seen := make(map[string]int, len(drafts)) // topic + "\x00" + text_norm -> question number
	keepTexts := make([]string, 0, len(drafts))

	for _, d := range drafts {
		keepTexts = append(keepTexts, d.Text)
		norm := model.NormalizeQuestionText(d.Text)
		key := d.Topic + "\x00" + norm

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

		existing, err := tx.FindByTopicAndTextNorm(d.Topic, norm)
		if err != nil {
			return Result{}, fmt.Errorf("question %d: find duplicate: %w", d.Number, err)
		}
		if existing != nil {
			if opts.Mode == ModeUpdateExisting {
				// The new text may equal another row's exact text (a legacy
				// near-duplicate in another topic); updating would violate
				// the (course_id, text) unique index.
				other, err := tx.FindByCourseAndText(opts.CourseID, d.Text)
				if err != nil {
					return Result{}, fmt.Errorf("question %d: find same text: %w", d.Number, err)
				}
				if other != nil && other.ID != existing.ID {
					res.skip(d.Number, "QImportWarnDupOtherTopic", map[string]any{"ID": other.ID, "Topic": other.Topic})
					continue
				}
				q.ID = existing.ID
				if err := tx.Update(q); err != nil {
					return Result{}, fmt.Errorf("question %d: update #%d: %w", d.Number, existing.ID, err)
				}
				res.Updated++
			} else {
				res.skip(d.Number, "QImportWarnDupInDB", map[string]any{"ID": existing.ID, "Topic": d.Topic})
			}
			continue
		}

		// Same exact text in another topic: the (course_id, text) unique
		// index would reject the insert, so skip it with a warning.
		other, err := tx.FindByCourseAndText(opts.CourseID, d.Text)
		if err != nil {
			return Result{}, fmt.Errorf("question %d: find same text: %w", d.Number, err)
		}
		if other != nil {
			res.skip(d.Number, "QImportWarnDupOtherTopic", map[string]any{"ID": other.ID, "Topic": other.Topic})
			continue
		}

		if _, err := tx.Insert(q); err != nil {
			return Result{}, fmt.Errorf("question %d: insert: %w", d.Number, err)
		}
		res.Inserted++
	}

	if len(opts.OldTexts) > 0 {
		if err := tx.DeleteUnusedByTexts(opts.CourseID, opts.OldTexts, keepTexts); err != nil {
			return Result{}, fmt.Errorf("delete replaced questions: %w", err)
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
