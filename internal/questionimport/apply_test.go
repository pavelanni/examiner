package questionimport

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// fakeTx is an in-memory Tx. Like the real store, it allows one active
// question per (course, topic, normalized text).
type fakeTx struct {
	rows      []model.Question
	nextID    int64
	insertErr error
	deletes   []string // "topic|text" per DeleteUnused call
	files     []FileRecord
}

func (f *fakeTx) FindByTopicAndTextNorm(courseID int64, topic, norm string) (*model.Question, error) {
	for i := range f.rows {
		if f.rows[i].CourseID == courseID && f.rows[i].Topic == topic && f.rows[i].TextNorm == norm {
			q := f.rows[i]
			return &q, nil
		}
	}
	return nil, nil
}

func (f *fakeTx) Insert(q model.Question) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	if existing, _ := f.FindByTopicAndTextNorm(q.CourseID, q.Topic, q.TextNorm); existing != nil {
		return 0, fmt.Errorf("UNIQUE constraint failed")
	}
	f.nextID++
	q.ID = f.nextID
	f.rows = append(f.rows, q)
	return q.ID, nil
}

func (f *fakeTx) Update(q model.Question) error {
	for i := range f.rows {
		if f.rows[i].ID == q.ID {
			f.rows[i] = q
			return nil
		}
	}
	return errors.New("no rows")
}

func (f *fakeTx) DeleteUnused(_ int64, topic, text string) error {
	f.deletes = append(f.deletes, topic+"|"+text)
	return nil
}

func (f *fakeTx) SetImportedFileHash(name, hash string) error {
	f.files = append(f.files, FileRecord{Name: name, Hash: hash})
	return nil
}

// parsed builds drafts from "text|topic" specs; rubric is "r-<text>".
func parsed(t *testing.T, specs ...string) []Draft {
	t.Helper()
	var qs []string
	for _, s := range specs {
		text, topic, _ := strings.Cut(s, "|")
		qs = append(qs, fmt.Sprintf(`{"text":%q,"difficulty":"easy","topic":%q,"rubric":"r-%s","model_answer":"a","max_points":5}`, text, topic, text))
	}
	drafts, errs := Parse([]byte("[" + strings.Join(qs, ",") + "]"))
	if len(errs) != 0 {
		t.Fatalf("Parse: %v", errs)
	}
	return drafts
}

func warnCodes(ws []Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, fmt.Sprintf("%d:%s", w.Question, w.Code))
	}
	return out
}

func TestApplyInsertsAll(t *testing.T) {
	tx := &fakeTx{}
	res, err := Apply(tx, parsed(t, "A|x", "B|x", "C|y"), Options{CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 3 || res.Updated != 0 || res.Skipped != 0 || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
	for _, q := range tx.rows {
		if q.CourseID != 1 || q.TextNorm != model.NormalizeQuestionText(q.Text) || q.MaxPoints != 5 {
			t.Errorf("row = %+v", q)
		}
	}
}

func TestApplyDupInFile(t *testing.T) {
	tx := &fakeTx{}
	res, err := Apply(tx, parsed(t, "What is X?|x", "B|x", "  what is x |x"), Options{CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 2 || res.Skipped != 1 {
		t.Errorf("result = %+v", res)
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"3:QImportWarnDupInFile"}) {
		t.Errorf("warnings = %v", got)
	}
	if res.Warnings[0].Params["PrevN"] != 1 || res.Warnings[0].Params["N"] != 3 || res.Warnings[0].Params["Topic"] != "x" {
		t.Errorf("params = %v", res.Warnings[0].Params)
	}
}

func TestApplySameTextDifferentTopic(t *testing.T) {
	tx := existingTx() // "A" in topic x
	res, err := Apply(tx, parsed(t, "A|y", "A|z"), Options{CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 2 || res.Skipped != 0 || len(tx.rows) != 3 {
		t.Errorf("result = %+v, rows = %+v; want both inserted", res, tx.rows)
	}
}

func existingTx() *fakeTx {
	return &fakeTx{nextID: 7, rows: []model.Question{{
		ID: 7, CourseID: 1, Text: "A", TextNorm: "a", Topic: "x", Difficulty: "easy", Rubric: "old", ModelAnswer: "a", MaxPoints: 1,
	}}}
}

func TestApplyDupInDBSkip(t *testing.T) {
	tx := existingTx()
	res, err := Apply(tx, parsed(t, "a?|x"), Options{Mode: ModeSkipExisting, CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.Inserted != 0 || tx.rows[0].Rubric != "old" {
		t.Errorf("result = %+v, row = %+v", res, tx.rows[0])
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"1:QImportWarnDupInDB"}) || res.Warnings[0].Params["ID"] != int64(7) {
		t.Errorf("warnings = %v %v", got, res.Warnings)
	}
}

func TestApplyDupInDBUpdate(t *testing.T) {
	tx := existingTx()
	res, err := Apply(tx, parsed(t, "A|x"), Options{Mode: ModeUpdateExisting, CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || res.Inserted != 0 || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(tx.rows) != 1 || tx.rows[0].ID != 7 || tx.rows[0].Rubric != "r-A" || tx.rows[0].MaxPoints != 5 {
		t.Errorf("row = %+v", tx.rows[0])
	}
}

func TestApplyNotEditableSkipped(t *testing.T) {
	tx := existingTx()
	var asked []int64
	res, err := Apply(tx, parsed(t, "a?|x", "B|x"), Options{Mode: ModeUpdateExisting, CourseID: 1,
		Editable: func(q model.Question) bool { asked = append(asked, q.ID); return false }})
	if err != nil {
		t.Fatal(err)
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"1:QImportWarnDupInDB"}) || res.Skipped != 1 || res.Inserted != 1 {
		t.Errorf("result = %+v", res)
	}
	if tx.rows[0].Rubric != "old" || !slices.Equal(asked, []int64{7}) {
		t.Errorf("row = %+v, asked = %v", tx.rows[0], asked)
	}
	if !slices.Equal(res.Saved, []int{2}) {
		t.Errorf("saved = %v, want [2]", res.Saved)
	}
}

func TestApplyOld(t *testing.T) {
	tx := &fakeTx{}
	old := parsed(t, "A|x", "B|x", "c?|y", "A|z")
	if _, err := Apply(tx, parsed(t, "a|x", "C|y"), Options{CourseID: 1, Old: old}); err != nil {
		t.Fatal(err)
	}
	// "A|x" and "c?|y" keep their identity; "B|x" is gone and "A" in z
	// is a different question from "a" in x.
	if !slices.Equal(tx.deletes, []string{"x|B", "z|A"}) {
		t.Errorf("deletes = %v", tx.deletes)
	}

	tx = &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{CourseID: 1}); err != nil {
		t.Fatal(err)
	}
	if len(tx.deletes) != 0 {
		t.Errorf("deletes without Old = %v", tx.deletes)
	}
}

func TestApplySaved(t *testing.T) {
	tx := existingTx()
	res, err := Apply(tx, parsed(t, "N|x", "N|x", "a|x", "M|y"), Options{Mode: ModeSkipExisting, CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Saved, []int{1, 4}) {
		t.Errorf("saved = %v, want [1 4] (2 duplicates 1, 3 exists)", res.Saved)
	}
	res, err = Apply(tx, parsed(t, "A|x"), Options{Mode: ModeUpdateExisting, CourseID: 1})
	if err != nil || !slices.Equal(res.Saved, []int{1}) {
		t.Errorf("update: saved = %v, %v; want [1]", res.Saved, err)
	}
}

func TestApplyRecordsFile(t *testing.T) {
	tx := &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{CourseID: 1, File: &FileRecord{Name: "f.json", Hash: "h"}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tx.files, []FileRecord{{"f.json", "h"}}) {
		t.Errorf("files = %v", tx.files)
	}

	tx = &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{CourseID: 1}); err != nil {
		t.Fatal(err)
	}
	if len(tx.files) != 0 {
		t.Errorf("files without File = %v", tx.files)
	}
}

func TestApplyPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	tx := &fakeTx{insertErr: boom}
	res, err := Apply(tx, parsed(t, "A|x"), Options{CourseID: 1, File: &FileRecord{Name: "f", Hash: "h"}})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if res.Inserted != 0 || len(tx.files) != 0 {
		t.Errorf("result = %+v, files = %v; want zero result, no file record", res, tx.files)
	}
}
