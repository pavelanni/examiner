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
	FindByID(id int64) (*model.Question, error)
	// The finders skip the row with excludeID; 0 excludes nothing.
	FindByTopicAndTextNorm(topic, textNorm string, excludeID int64) (*model.Question, error)
	FindByCourseAndText(courseID int64, text string, excludeID int64) (*model.Question, error)
	Insert(q model.Question) (int64, error)
	Update(q model.Question) error
	DeleteUnusedByIDs(ownerID int64, ids []int64) error
	SetImportedFileHash(name, hash string) error
	UserDisplayName(id int64) (string, error)
}

// Mode says what to do with a question that already exists in the topic.
type Mode int

const (
	// ModeSkipExisting skips it with a warning (admin import, startup).
	ModeSkipExisting Mode = iota
	// ModeUpdateExisting updates it in place (admin import on request).
	ModeUpdateExisting
	// ModeAsk leaves every duplicate to Options.Decisions (teacher
	// constructor). While any duplicate has no usable decision, Apply
	// fails with *DuplicatesError.
	ModeAsk
)

// Decision is what to do with one duplicate in ModeAsk.
type Decision int

const (
	// DecisionNone means no decision yet.
	DecisionNone Decision = iota
	// DecisionSkip does not save the question.
	DecisionSkip
	// DecisionUpdate overwrites the matching stored question. It is
	// allowed only when Duplicate.CanUpdate is set.
	DecisionUpdate
)

// FileRecord is the imported file to record with the questions.
type FileRecord struct {
	Name, Hash string
}

// Options controls Apply.
type Options struct {
	Mode     Mode
	CourseID int64
	// OldIDs are the stored questions of the file being edited. Those no
	// draft keeps are deleted if the actor owns them and no session uses them.
	OldIDs    []int64
	File      *FileRecord      // recorded in imported_files inside the transaction
	Decisions map[int]Decision // ModeAsk: by question number

	Actor     *model.User // who imports; owns inserted questions
	Published bool        // publication state of inserted and updated questions
}

// ConflictError lists questions that cannot be saved as they are: they
// belong to someone the actor may not edit, or an edit would make them
// duplicate another question. Callers show it like validation errors.
type ConflictError struct {
	Errs []FieldError
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%d question(s) conflict with stored questions", len(e.Errs))
}

// Duplicate is a question that matches a stored question or an earlier
// question in the file and needs a decision in ModeAsk.
type Duplicate struct {
	Question  int
	Code      string // i18n message ID
	Params    map[string]any
	CanUpdate bool // DecisionUpdate is allowed; DecisionSkip always is
}

// DuplicatesError lists the duplicates still waiting for a decision.
// Apply returns it before writing anything that would survive a rollback.
type DuplicatesError struct {
	Dups []Duplicate
}

func (e *DuplicatesError) Error() string {
	return fmt.Sprintf("%d duplicate question(s) need a decision", len(e.Dups))
}

// Result counts what Apply did.
type Result struct {
	Inserted, Updated, Skipped int
	Warnings                   []Warning // one per skipped question
}

// Apply writes validated questions through tx. Run it inside one
// transaction: on error the caller rolls back and the Result is discarded.
func Apply(tx Tx, drafts []Draft, opts Options) (Result, error) {
	if opts.Actor == nil {
		return Result{}, errors.New("import without an acting user")
	}
	a := applier{tx: tx, opts: opts, kept: map[int64]bool{}}
	seen := make(map[string]int, len(drafts)) // topic + "\x00" + text_norm -> question number

	for _, d := range drafts {
		norm := model.NormalizeQuestionText(d.Text)
		key := d.Topic + "\x00" + norm

		if prev, ok := seen[key]; ok {
			a.duplicate(d.Number, "QImportWarnDupInFile", map[string]any{"PrevN": prev, "Topic": d.Topic})
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
		if err := a.apply(d, q); err != nil {
			return Result{}, fmt.Errorf("question %d: %w", d.Number, err)
		}
	}

	if len(a.conflicts) > 0 {
		return Result{}, &ConflictError{Errs: a.conflicts}
	}
	if len(a.pending) > 0 {
		return Result{}, &DuplicatesError{Dups: a.pending}
	}
	var drop []int64
	for _, id := range opts.OldIDs {
		if !a.kept[id] {
			drop = append(drop, id)
		}
	}
	if len(drop) > 0 {
		if err := tx.DeleteUnusedByIDs(opts.Actor.ID, drop); err != nil {
			return Result{}, fmt.Errorf("delete replaced questions: %w", err)
		}
	}
	if opts.File != nil {
		if err := tx.SetImportedFileHash(opts.File.Name, opts.File.Hash); err != nil {
			return Result{}, fmt.Errorf("record imported file: %w", err)
		}
	}
	return a.res, nil
}

// applier holds the state of one Apply call.
type applier struct {
	tx        Tx
	opts      Options
	res       Result
	conflicts []FieldError
	pending   []Duplicate
	kept      map[int64]bool // stored questions a draft updated
}

// apply saves one question that is not a duplicate within the file.
func (a *applier) apply(d Draft, q model.Question) error {
	tx, opts := a.tx, a.opts

	// An edit of a stored question (constructor): update it by ID.
	if d.ID != 0 {
		target, err := tx.FindByID(d.ID)
		if err != nil {
			return fmt.Errorf("find #%d: %w", d.ID, err)
		}
		// A question that is gone or not the actor's is saved as a new one.
		if target != nil && access.CanEditQuestion(opts.Actor, *target) {
			other, err := a.findMatch(d, q.TextNorm, target.ID)
			if err != nil {
				return err
			}
			if other != nil {
				a.conflicts = append(a.conflicts, FieldError{Question: d.Number, Code: "QImportErrEditDup",
					Params: map[string]any{"ID": other.ID, "Topic": other.Topic}})
				return nil
			}
			return a.update(q, target)
		}
	}

	// Prefer the row with exactly this text: legacy data may hold several
	// rows with the same normalized text, and another owner's near-duplicate
	// must not hide the actor's own row.
	existing, err := tx.FindByCourseAndText(opts.CourseID, d.Text, 0)
	if err != nil {
		return fmt.Errorf("find same text: %w", err)
	}
	if existing == nil || existing.Topic != d.Topic {
		existing, err = tx.FindByTopicAndTextNorm(d.Topic, q.TextNorm, 0)
		if err != nil {
			return fmt.Errorf("find duplicate: %w", err)
		}
	}
	if existing != nil {
		return a.existing(d, q, existing)
	}

	// Same exact text in another topic: the (course_id, text) unique
	// index would reject the insert, so the question can only be skipped.
	other, err := tx.FindByCourseAndText(opts.CourseID, d.Text, 0)
	if err != nil {
		return fmt.Errorf("find same text: %w", err)
	}
	if other != nil {
		a.duplicate(d.Number, "QImportWarnDupOtherTopic", map[string]any{"ID": other.ID, "Topic": other.Topic})
		return nil
	}

	if _, err := tx.Insert(q); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	a.res.Inserted++
	return nil
}

// existing handles a new question that matches stored question e in its topic.
func (a *applier) existing(d Draft, q model.Question, e *model.Question) error {
	tx, opts := a.tx, a.opts
	if opts.Mode == ModeSkipExisting {
		a.skip(d.Number, "QImportWarnDupInDB", map[string]any{"ID": e.ID, "Topic": d.Topic})
		return nil
	}

	canEdit := access.CanEditQuestion(opts.Actor, *e)
	// The new text may equal another row's exact text (a legacy
	// near-duplicate in another topic); updating would violate the
	// (course_id, text) unique index.
	var collision *model.Question
	if canEdit {
		var err error
		if collision, err = tx.FindByCourseAndText(opts.CourseID, d.Text, e.ID); err != nil {
			return fmt.Errorf("find same text: %w", err)
		}
	}

	if opts.Mode == ModeUpdateExisting {
		switch {
		case !canEdit:
			owner, err := a.ownerName(e)
			if err != nil {
				return err
			}
			a.conflicts = append(a.conflicts, FieldError{Question: d.Number, Code: "QImportErrNotOwner",
				Params: map[string]any{"ID": e.ID, "Owner": owner, "Topic": d.Topic}})
		case collision != nil:
			a.skip(d.Number, "QImportWarnDupOtherTopic", map[string]any{"ID": collision.ID, "Topic": collision.Topic})
		default:
			return a.update(q, e)
		}
		return nil
	}

	// ModeAsk.
	canUpdate := canEdit && collision == nil
	switch a.opts.Decisions[d.Number] {
	case DecisionSkip:
		a.skip(d.Number, "QImportWarnDupInDB", map[string]any{"ID": e.ID, "Topic": d.Topic})
		return nil
	case DecisionUpdate:
		if canUpdate {
			return a.update(q, e)
		}
	}
	params := map[string]any{"N": d.Number, "ID": e.ID, "Topic": d.Topic}
	code := "QDupInDB"
	if e.OwnerID != opts.Actor.ID {
		owner, err := a.ownerName(e)
		if err != nil {
			return err
		}
		params["Owner"] = owner
		code = "QDupInDBOther"
	}
	a.pending = append(a.pending, Duplicate{Question: d.Number, Code: code, Params: params, CanUpdate: canUpdate})
	return nil
}

// findMatch returns a stored question other than excludeID that the draft
// duplicates: one with exactly its text or one in its topic with the same
// normalized text.
func (a *applier) findMatch(d Draft, norm string, excludeID int64) (*model.Question, error) {
	other, err := a.tx.FindByCourseAndText(a.opts.CourseID, d.Text, excludeID)
	if err != nil {
		return nil, fmt.Errorf("find same text: %w", err)
	}
	if other != nil {
		return other, nil
	}
	other, err = a.tx.FindByTopicAndTextNorm(d.Topic, norm, excludeID)
	if err != nil {
		return nil, fmt.Errorf("find duplicate: %w", err)
	}
	return other, nil
}

// update overwrites stored question e with q, keeping e's owner.
func (a *applier) update(q model.Question, e *model.Question) error {
	q.ID = e.ID
	q.OwnerID = e.OwnerID
	// Publishing is the owner's decision: an admin fixing someone
	// else's question keeps its publication state.
	if e.OwnerID != a.opts.Actor.ID {
		q.Published = e.Published
	}
	if err := a.tx.Update(q); err != nil {
		return fmt.Errorf("update #%d: %w", e.ID, err)
	}
	a.res.Updated++
	a.kept[e.ID] = true
	return nil
}

// duplicate handles a question that can only be skipped: it is skipped with
// warning code, or in ModeAsk, left pending until the actor agrees.
func (a *applier) duplicate(n int, code string, params map[string]any) {
	if a.opts.Mode == ModeAsk && a.opts.Decisions[n] != DecisionSkip {
		params["N"] = n
		a.pending = append(a.pending, Duplicate{Question: n, Code: askCode[code], Params: params})
		return
	}
	a.skip(n, code, params)
}

// askCode maps a skip warning to the message asking about the same duplicate.
var askCode = map[string]string{
	"QImportWarnDupInFile":     "QDupInFile",
	"QImportWarnDupOtherTopic": "QDupOtherTopic",
}

func (a *applier) ownerName(e *model.Question) (string, error) {
	owner, err := a.tx.UserDisplayName(e.OwnerID)
	if err != nil {
		return "", fmt.Errorf("owner name: %w", err)
	}
	return owner, nil
}

func (a *applier) skip(n int, code string, params map[string]any) {
	params["N"] = n
	a.res.Skipped++
	a.res.Warnings = append(a.res.Warnings, Warning{Question: n, Code: code, Params: params})
}
