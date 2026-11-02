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
	deletes   []fakeDelete
	files     []FileRecord
	users     map[int64]string // display names
}

type fakeDelete struct {
	owner int64
	ids   []int64
}

var testAdmin = &model.User{ID: 1, Role: model.UserRoleAdmin}

func (f *fakeTx) FindByID(id int64) (*model.Question, error) {
	for i := range f.rows {
		if f.rows[i].ID == id {
			q := f.rows[i]
			return &q, nil
		}
	}
	return nil, nil
}

func (f *fakeTx) FindByTopicAndTextNorm(topic, norm string, excludeID int64) (*model.Question, error) {
	for i := range f.rows {
		if f.rows[i].Topic == topic && f.rows[i].TextNorm == norm && f.rows[i].ID != excludeID {
			q := f.rows[i]
			return &q, nil
		}
	}
	return nil, nil
}

func (f *fakeTx) FindByCourseAndText(courseID int64, text string, excludeID int64) (*model.Question, error) {
	for i := range f.rows {
		if f.rows[i].CourseID == courseID && f.rows[i].Text == text && f.rows[i].ID != excludeID {
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
	if existing, _ := f.FindByCourseAndText(q.CourseID, q.Text, 0); existing != nil {
		return 0, fmt.Errorf("UNIQUE constraint failed")
	}
	f.nextID++
	q.ID = f.nextID
	f.rows = append(f.rows, q)
	return q.ID, nil
}

func (f *fakeTx) Update(q model.Question) error {
	if other, _ := f.FindByCourseAndText(q.CourseID, q.Text, q.ID); other != nil {
		return fmt.Errorf("UNIQUE constraint failed")
	}
	for i := range f.rows {
		if f.rows[i].ID == q.ID {
			f.rows[i] = q
			return nil
		}
	}
	return errors.New("no rows")
}

func (f *fakeTx) DeleteUnusedByIDs(ownerID int64, ids []int64) error {
	f.deletes = append(f.deletes, fakeDelete{ownerID, ids})
	return nil
}

func (f *fakeTx) UserDisplayName(id int64) (string, error) {
	return f.users[id], nil
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
	res, err := Apply(tx, parsed(t, "A|x", "B|x", "C|y"), Options{Actor: testAdmin, CourseID: 1})
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
	res, err := Apply(tx, parsed(t, "What is X?|x", "B|x", "  what is x |x"), Options{Actor: testAdmin, CourseID: 1})
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
	res, err := Apply(tx, parsed(t, "A|x", "A|y"), Options{Actor: testAdmin, CourseID: 1})
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
	res, err := Apply(tx, parsed(t, "a?|x"), Options{Actor: testAdmin, Mode: ModeSkipExisting, CourseID: 1})
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
	res, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, Mode: ModeUpdateExisting, CourseID: 1})
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
	res, err := Apply(tx, parsed(t, "A|y"), Options{Actor: testAdmin, Mode: ModeUpdateExisting, CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"1:QImportWarnDupOtherTopic"}) || res.Skipped != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestApplyOldIDs(t *testing.T) {
	tx := existingTx() // row 7 "A"
	drafts := withIDs(parsed(t, "A|x"), 7)
	if _, err := Apply(tx, drafts, Options{Actor: testAdmin, Mode: ModeAsk, CourseID: 1, OldIDs: []int64{7, 9}}); err != nil {
		t.Fatal(err)
	}
	if len(tx.deletes) != 1 || !slices.Equal(tx.deletes[0].ids, []int64{9}) {
		t.Errorf("deletes = %v; want only the dropped #9", tx.deletes)
	}

	tx = existingTx()
	if _, err := Apply(tx, drafts, Options{Actor: testAdmin, Mode: ModeAsk, CourseID: 1, OldIDs: []int64{7}}); err != nil {
		t.Fatal(err)
	}
	if len(tx.deletes) != 0 {
		t.Errorf("deletes with every old question kept = %v", tx.deletes)
	}

	tx = &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, CourseID: 1}); err != nil {
		t.Fatal(err)
	}
	if len(tx.deletes) != 0 {
		t.Errorf("deletes without OldIDs = %v", tx.deletes)
	}
}

func TestApplyRecordsFile(t *testing.T) {
	tx := &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, CourseID: 1, File: &FileRecord{Name: "f.json", Hash: "h"}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tx.files, []FileRecord{{"f.json", "h"}}) {
		t.Errorf("files = %v", tx.files)
	}

	tx = &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, CourseID: 1}); err != nil {
		t.Fatal(err)
	}
	if len(tx.files) != 0 {
		t.Errorf("files without File = %v", tx.files)
	}
}

func TestApplyPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	tx := &fakeTx{insertErr: boom}
	res, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, CourseID: 1, File: &FileRecord{Name: "f", Hash: "h"}})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if res.Inserted != 0 || len(tx.files) != 0 {
		t.Errorf("result = %+v, files = %v; want zero result, no file record", res, tx.files)
	}
}

func TestApplyUpdateWouldCollideWithOtherTopic(t *testing.T) {
	tx := &fakeTx{nextID: 8, rows: []model.Question{
		{ID: 7, CourseID: 1, Text: "What is OOP?", TextNorm: "what is oop", Topic: "x", Difficulty: "easy", Rubric: "r", ModelAnswer: "a", MaxPoints: 1},
		{ID: 8, CourseID: 1, Text: "What is OOP", TextNorm: "what is oop", Topic: "y", Difficulty: "easy", Rubric: "r", ModelAnswer: "a", MaxPoints: 1},
	}}
	res, err := Apply(tx, parsed(t, "What is OOP|x"), Options{Actor: testAdmin, Mode: ModeUpdateExisting, CourseID: 1})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"1:QImportWarnDupOtherTopic"}) || res.Skipped != 1 || res.Warnings[0].Params["ID"] != int64(8) {
		t.Errorf("result = %+v", res)
	}
	if tx.rows[0].Text != "What is OOP?" {
		t.Errorf("row 7 changed: %+v", tx.rows[0])
	}
}

var (
	teacher1 = &model.User{ID: 11, Role: model.UserRoleTeacher}
	teacher2 = &model.User{ID: 12, Role: model.UserRoleTeacher}
)

// ownedTx has row 17 ("A", topic x) owned by teacher2 "Ivanov".
func ownedTx() *fakeTx {
	return &fakeTx{nextID: 20, users: map[int64]string{12: "Ivanov"}, rows: []model.Question{{
		ID: 17, CourseID: 1, Text: "A", TextNorm: "a", Topic: "x", Difficulty: "easy",
		Rubric: "orig", ModelAnswer: "a", MaxPoints: 1, OwnerID: 12, Published: true,
	}}}
}

func TestApplyNilActor(t *testing.T) {
	tx := &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{CourseID: 1}); err == nil {
		t.Fatal("nil actor: want error")
	}
	if len(tx.rows) != 0 {
		t.Errorf("rows written: %v", tx.rows)
	}
}

func TestApplyInsertSetsOwnerAndPublished(t *testing.T) {
	tx := &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{Actor: teacher1, CourseID: 1, Published: false}); err != nil {
		t.Fatal(err)
	}
	if tx.rows[0].OwnerID != 11 || tx.rows[0].Published {
		t.Errorf("row = %+v; want owner 11, unpublished", tx.rows[0])
	}
}

func TestApplyUpdatesOwnQuestion(t *testing.T) {
	tx := ownedTx()
	res, err := Apply(tx, parsed(t, "A|x"), Options{Actor: teacher2, Mode: ModeUpdateExisting, CourseID: 1, Published: false})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || tx.rows[0].OwnerID != 12 || tx.rows[0].Published || tx.rows[0].Rubric != "r-A" {
		t.Errorf("result %+v, row %+v", res, tx.rows[0])
	}
}

func TestApplyConflictWithOtherOwner(t *testing.T) {
	tx := ownedTx()
	_, err := Apply(tx, parsed(t, "A|x"), Options{Actor: teacher1, Mode: ModeUpdateExisting, CourseID: 1,
		OldIDs: []int64{99}, File: &FileRecord{Name: "f", Hash: "h"}})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *ConflictError", err)
	}
	if got := keys(ce.Errs); !sameKeys(got, []errKey{{1, "", "QImportErrNotOwner"}}) {
		t.Errorf("errs = %v", got)
	}
	p := ce.Errs[0].Params
	if p["ID"] != int64(17) || p["Owner"] != "Ivanov" || p["Topic"] != "x" {
		t.Errorf("params = %v", p)
	}
	if tx.rows[0].Rubric != "orig" || len(tx.deletes) != 0 || len(tx.files) != 0 {
		t.Errorf("writes after conflict: row %+v, deletes %v, files %v", tx.rows[0], tx.deletes, tx.files)
	}
}

func TestApplyPrefersExactTextOverOtherOwnersNearDuplicate(t *testing.T) {
	tx := ownedTx() // row 17 "A" owned by teacher2
	tx.rows = append(tx.rows, model.Question{ID: 18, CourseID: 1, Text: "A?", TextNorm: "a", Topic: "x", OwnerID: 11})
	res, err := Apply(tx, parsed(t, "A?|x"), Options{Actor: teacher1, Mode: ModeUpdateExisting, CourseID: 1})
	if err != nil {
		t.Fatalf("err = %v; teacher1's own exact-text row should be updated", err)
	}
	if res.Updated != 1 || tx.rows[1].Rubric != "r-A?" || tx.rows[0].Rubric != "orig" {
		t.Errorf("result %+v, rows %+v", res, tx.rows)
	}
}

func TestApplyTwoConflicts(t *testing.T) {
	tx := ownedTx()
	tx.rows = append(tx.rows, model.Question{ID: 18, CourseID: 1, Text: "B", TextNorm: "b", Topic: "x", OwnerID: 12})
	_, err := Apply(tx, parsed(t, "A|x", "New|x", "B|x"), Options{Actor: teacher1, Mode: ModeUpdateExisting, CourseID: 1})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	if got := keys(ce.Errs); !sameKeys(got, []errKey{{1, "", "QImportErrNotOwner"}, {3, "", "QImportErrNotOwner"}}) {
		t.Errorf("errs = %v", got)
	}
}

func TestApplyAdminActorUpdatesAnyQuestion(t *testing.T) {
	tx := ownedTx()
	res, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, Mode: ModeUpdateExisting, CourseID: 1, Published: true})
	if err != nil || res.Updated != 1 || tx.rows[0].OwnerID != 12 || tx.rows[0].Rubric != "r-A" {
		t.Errorf("result %+v, %v, row %+v", res, err, tx.rows[0])
	}
}

func TestApplySkipModeIgnoresOwner(t *testing.T) {
	tx := ownedTx()
	res, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, Mode: ModeSkipExisting, CourseID: 1, Published: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"1:QImportWarnDupInDB"}) || tx.rows[0].Rubric != "orig" {
		t.Errorf("warnings %v, row %+v", got, tx.rows[0])
	}
}

func TestApplyDeleteScopedToActor(t *testing.T) {
	tx := &fakeTx{}
	if _, err := Apply(tx, parsed(t, "A|x"), Options{Actor: teacher1, Mode: ModeUpdateExisting, CourseID: 1, OldIDs: []int64{99}}); err != nil {
		t.Fatal(err)
	}
	if len(tx.deletes) != 1 || tx.deletes[0].owner != 11 {
		t.Errorf("deletes = %+v; want owner 11", tx.deletes)
	}
}

func TestApplyForeignRowKeepsPublished(t *testing.T) {
	tx := ownedTx()
	tx.rows[0].Published = false
	if _, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, Mode: ModeUpdateExisting, CourseID: 1, Published: true}); err != nil {
		t.Fatal(err)
	}
	if tx.rows[0].Published || tx.rows[0].Rubric != "r-A" {
		t.Errorf("row = %+v; want rubric updated, publication kept", tx.rows[0])
	}
}

// withIDs sets the stored question each draft edits.
func withIDs(drafts []Draft, ids ...int64) []Draft {
	for i, id := range ids {
		drafts[i].ID = id
	}
	return drafts
}

// teacherTx has rows 7 "A" and 8 "B" in topic x, owned by teacher1.
func teacherTx() *fakeTx {
	return &fakeTx{nextID: 8, users: map[int64]string{11: "Petrov"}, rows: []model.Question{
		{ID: 7, CourseID: 1, Text: "A", TextNorm: "a", Topic: "x", Difficulty: "easy", Rubric: "old", ModelAnswer: "a", MaxPoints: 1, OwnerID: 11},
		{ID: 8, CourseID: 1, Text: "B", TextNorm: "b", Topic: "x", Difficulty: "easy", Rubric: "old", ModelAnswer: "a", MaxPoints: 1, OwnerID: 11},
	}}
}

func TestApplyEditByID(t *testing.T) {
	tx := teacherTx()
	res, err := Apply(tx, withIDs(parsed(t, "A, reworded|y"), 7), Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || res.Inserted != 0 || len(tx.rows) != 2 {
		t.Errorf("result %+v, rows %+v; want #7 updated in place", res, tx.rows)
	}
	if q := tx.rows[0]; q.Text != "A, reworded" || q.TextNorm != "a, reworded" || q.Topic != "y" || q.OwnerID != 11 {
		t.Errorf("row 7 = %+v", q)
	}
}

func TestApplyResaveByIDIsIdempotent(t *testing.T) {
	tx := teacherTx()
	for range 2 {
		res, err := Apply(tx, withIDs(parsed(t, "A|x", "B|x"), 7, 8), Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1})
		if err != nil {
			t.Fatal(err)
		}
		if res.Updated != 2 || res.Inserted != 0 || res.Skipped != 0 || len(tx.rows) != 2 {
			t.Errorf("result %+v, %d rows; want both updated, no new rows", res, len(tx.rows))
		}
	}
}

func TestApplyEditByIDCannotDuplicate(t *testing.T) {
	for name, spec := range map[string]string{"near-duplicate": "b?|x", "same text": "B|x"} {
		t.Run(name, func(t *testing.T) {
			tx := teacherTx()
			_, err := Apply(tx, withIDs(parsed(t, spec), 7), Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1})
			var ce *ConflictError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *ConflictError", err)
			}
			if got := keys(ce.Errs); !sameKeys(got, []errKey{{1, "", "QImportErrEditDup"}}) || ce.Errs[0].Params["ID"] != int64(8) {
				t.Errorf("errs = %v", ce.Errs)
			}
			if tx.rows[0].Text != "A" {
				t.Errorf("row 7 changed: %+v", tx.rows[0])
			}
		})
	}
}

func TestApplyForeignIDIsNewQuestion(t *testing.T) {
	tx := ownedTx() // row 17 "A" owned by teacher2
	_, err := Apply(tx, withIDs(parsed(t, "A|x"), 17), Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1})
	var de *DuplicatesError
	if !errors.As(err, &de) || len(de.Dups) != 1 || de.Dups[0].CanUpdate {
		t.Fatalf("err = %v; want one duplicate that cannot be updated", err)
	}
	if tx.rows[0].Rubric != "orig" {
		t.Errorf("teacher2's row changed: %+v", tx.rows[0])
	}
}

func TestApplyAskReportsDuplicates(t *testing.T) {
	tx := teacherTx()
	tx.rows = append(tx.rows, model.Question{ID: 17, CourseID: 1, Text: "C", TextNorm: "c", Topic: "x", OwnerID: 12})
	tx.users[12] = "Ivanov"
	drafts := parsed(t, "a?|x", "New|x", "c!|x", "New|x", "B|y")
	_, err := Apply(tx, drafts, Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1, File: &FileRecord{Name: "f", Hash: "h"}})
	var de *DuplicatesError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DuplicatesError", err)
	}
	type dup struct {
		n         int
		code      string
		canUpdate bool
	}
	var got []dup
	for _, d := range de.Dups {
		got = append(got, dup{d.Question, d.Code, d.CanUpdate})
	}
	want := []dup{{1, "QDupInDB", true}, {3, "QDupInDBOther", false}, {4, "QDupInFile", false}, {5, "QDupOtherTopic", false}}
	if !slices.Equal(got, want) {
		t.Errorf("dups = %v; want %v", got, want)
	}
	if p := de.Dups[1].Params; p["ID"] != int64(17) || p["Owner"] != "Ivanov" || p["N"] != 3 {
		t.Errorf("params = %v", p)
	}
	if len(tx.files) != 0 || len(tx.deletes) != 0 {
		t.Errorf("writes after pending duplicates: files %v, deletes %v", tx.files, tx.deletes)
	}
}

func TestApplyAskDecisions(t *testing.T) {
	drafts := func() []Draft { return parsed(t, "a?|x", "New|x", "new.|x", "B|y") }

	tx := teacherTx()
	res, err := Apply(tx, drafts(), Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1,
		Decisions: map[int]Decision{1: DecisionSkip, 3: DecisionSkip, 4: DecisionSkip}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 1 || res.Skipped != 3 || tx.rows[0].Text != "A" {
		t.Errorf("skip: result %+v, row 7 %+v", res, tx.rows[0])
	}
	if got := warnCodes(res.Warnings); !slices.Equal(got, []string{"1:QImportWarnDupInDB", "3:QImportWarnDupInFile", "4:QImportWarnDupOtherTopic"}) {
		t.Errorf("warnings = %v", got)
	}

	tx = teacherTx()
	res, err = Apply(tx, drafts(), Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1,
		Decisions: map[int]Decision{1: DecisionUpdate, 3: DecisionSkip, 4: DecisionSkip}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || tx.rows[0].Text != "a?" || tx.rows[0].Rubric != "r-a?" {
		t.Errorf("update: result %+v, row 7 %+v", res, tx.rows[0])
	}
}

func TestApplyAskUpdateKeptFromDeletion(t *testing.T) {
	tx := teacherTx()
	// The edited file had #7; its block is gone, but a new block matching
	// it was saved with "update", so #7 stays.
	_, err := Apply(tx, parsed(t, "a?|x"), Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1,
		OldIDs: []int64{7, 8}, Decisions: map[int]Decision{1: DecisionUpdate}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.deletes) != 1 || !slices.Equal(tx.deletes[0].ids, []int64{8}) {
		t.Errorf("deletes = %v; want only #8", tx.deletes)
	}
}

func TestApplyAskCannotUpdateOthersQuestion(t *testing.T) {
	tx := ownedTx() // row 17 "A" owned by teacher2
	_, err := Apply(tx, parsed(t, "A|x"), Options{Actor: teacher1, Mode: ModeAsk, CourseID: 1,
		Decisions: map[int]Decision{1: DecisionUpdate}})
	var de *DuplicatesError
	if !errors.As(err, &de) || len(de.Dups) != 1 {
		t.Fatalf("err = %v; an update of another owner's question must stay pending", err)
	}
	if tx.rows[0].Rubric != "orig" {
		t.Errorf("row changed: %+v", tx.rows[0])
	}
}

func TestApplyAskAdminMayUpdate(t *testing.T) {
	tx := ownedTx()
	res, err := Apply(tx, parsed(t, "A|x"), Options{Actor: testAdmin, Mode: ModeAsk, CourseID: 1,
		Decisions: map[int]Decision{1: DecisionUpdate}})
	if err != nil || res.Updated != 1 || tx.rows[0].OwnerID != 12 {
		t.Errorf("result %+v, %v, row %+v", res, err, tx.rows[0])
	}
}
