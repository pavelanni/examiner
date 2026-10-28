package questionimport

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
)

// fakeTx is an in-memory Tx. It enforces the (course_id, text) uniqueness
// the real store has.
type fakeTx struct {
	rows      []model.Question
	nextID    int64
	insertErr error
	deletes   [][2][]string // oldTexts, keepTexts per call
	files     []FileRecord
}

func (f *fakeTx) FindByTopicAndTextNorm(topic, norm string) (*model.Question, error) {
	for i := range f.rows {
		if f.rows[i].Topic == topic && f.rows[i].TextNorm == norm {
			q := f.rows[i]
			return &q, nil
		}
	}
	return nil, nil
}

func (f *fakeTx) FindByCourseAndText(courseID int64, text string) (*model.Question, error) {
	for i := range f.rows {
		if f.rows[i].CourseID == courseID && f.rows[i].Text == text {
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
	if existing, _ := f.FindByCourseAndText(q.CourseID, q.Text); existing != nil {
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

func (f *fakeTx) DeleteUnusedByTexts(_ int64, oldTexts, keepTexts []string) error {
	f.deletes = append(f.deletes, [2][]string{oldTexts, keepTexts})
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

func TestApplyDupInFileDifferentTopic(t *testing.T) {
	tx := &fakeTx{}
	res, err := Apply(tx, parsed(t, "A|x", "A|y"), Options{CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 1 || res.Skipped != 1 {
		t.Errorf("result = %+v", res)
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"2:QImportWarnDupOtherTopic"}) {
		t.Errorf("warnings = %v", got)
	}
	if res.Warnings[0].Params["Topic"] != "x" {
		t.Errorf("topic = %v, want the existing row's topic x", res.Warnings[0].Params["Topic"])
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

func TestApplyOtherTopicSkippedInUpdateMode(t *testing.T) {
	tx := existingTx()
	res, err := Apply(tx, parsed(t, "A|y"), Options{Mode: ModeUpdateExisting, CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"1:QImportWarnDupOtherTopic"}) || res.Skipped != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestApplyOldTexts(t *testing.T) {
	tx := &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{CourseID: 1, OldTexts: []string{"A", "B"}}); err != nil {
		t.Fatal(err)
	}
	if len(tx.deletes) != 1 || !slices.Equal(tx.deletes[0][0], []string{"A", "B"}) || !slices.Equal(tx.deletes[0][1], []string{"A"}) {
		t.Errorf("deletes = %v", tx.deletes)
	}

	tx = &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{CourseID: 1}); err != nil {
		t.Fatal(err)
	}
	if len(tx.deletes) != 0 {
		t.Errorf("deletes without OldTexts = %v", tx.deletes)
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
