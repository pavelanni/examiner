package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
)

func addTeacher(t *testing.T, s *store.Store, username string) int64 {
	t.Helper()
	id, err := s.CreateUser(model.User{Username: username, DisplayName: username, PasswordHash: "x", Role: model.UserRoleTeacher, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// addUnowned inserts a published question without an owner, like rows
// created before ownership existed.
func addUnowned(t *testing.T, s *store.Store, text string) int64 {
	t.Helper()
	var id int64
	err := s.InQuestionTx(t.Context(), func(tx *store.QuestionTx) error {
		var err error
		id, err = tx.Insert(model.Question{CourseID: 1, Text: text, Difficulty: "easy", Topic: "t", Rubric: "r",
			ModelAnswer: "a", MaxPoints: 5, Published: true})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func writeQuestionsFile(t *testing.T, dir, name string, texts ...string) {
	t.Helper()
	content := "["
	for i, text := range texts {
		if i > 0 {
			content += ","
		}
		content += q(text, "r", "5")
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"]"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ownerOf(t *testing.T, s *store.Store, id int64) int64 {
	t.Helper()
	got, err := s.GetQuestion(id)
	if err != nil {
		t.Fatal(err)
	}
	return got.OwnerID
}

func TestBackfillAssignsFromFiles(t *testing.T) {
	_, s := loadEnv(t)
	admin, _ := s.FirstAdminID()
	ivan := addTeacher(t, s, "ivan")
	olga := addTeacher(t, s, "olga")
	q1, q2, q3 := addUnowned(t, s, "Q1"), addUnowned(t, s, "Q2"), addUnowned(t, s, "Q3")
	dir := t.TempDir()
	writeQuestionsFile(t, dir, "teacher_ivan_mid.json", "Q1")
	writeQuestionsFile(t, dir, "teacher_olga_final.json", "q2?") // matched by normalized text

	if err := backfillQuestionOwners(s, dir); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]int64{q1: ivan, q2: olga, q3: admin} {
		if got := ownerOf(t, s, id); got != want {
			t.Errorf("question %d owner = %d, want %d", id, got, want)
		}
	}
}

func TestBackfillSharedQuestionGoesToAdmin(t *testing.T) {
	_, s := loadEnv(t)
	admin, _ := s.FirstAdminID()
	addTeacher(t, s, "ivan")
	addTeacher(t, s, "olga")
	q1 := addUnowned(t, s, "Q1")
	dir := t.TempDir()
	writeQuestionsFile(t, dir, "teacher_ivan_mid.json", "Q1")
	writeQuestionsFile(t, dir, "teacher_olga_final.json", "Q1")
	if err := backfillQuestionOwners(s, dir); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, s, q1); got != admin {
		t.Errorf("owner = %d, want admin %d", got, admin)
	}
}

func TestBackfillUnknownUserIgnored(t *testing.T) {
	_, s := loadEnv(t)
	admin, _ := s.FirstAdminID()
	q1 := addUnowned(t, s, "Q1")
	dir := t.TempDir()
	writeQuestionsFile(t, dir, "teacher_ghost_x.json", "Q1")
	if err := backfillQuestionOwners(s, dir); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, s, q1); got != admin {
		t.Errorf("owner = %d, want admin %d", got, admin)
	}
}

func TestBackfillLongestUsernamePrefix(t *testing.T) {
	_, s := loadEnv(t)
	ivan := addTeacher(t, s, "ivan")
	q1 := addUnowned(t, s, "Q1")
	dir := t.TempDir()
	writeQuestionsFile(t, dir, "teacher_ivan_mid_term.json", "Q1")
	if err := backfillQuestionOwners(s, dir); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, s, q1); got != ivan {
		t.Errorf("only ivan exists: owner = %d, want %d", got, ivan)
	}

	_, s = loadEnv(t)
	addTeacher(t, s, "ivan")
	ivanMid := addTeacher(t, s, "ivan_mid")
	q1 = addUnowned(t, s, "Q1")
	if err := backfillQuestionOwners(s, dir); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, s, q1); got != ivanMid {
		t.Errorf("ivan and ivan_mid exist: owner = %d, want ivan_mid %d", got, ivanMid)
	}
}

func TestBackfillIdempotent(t *testing.T) {
	_, s := loadEnv(t)
	ivan := addTeacher(t, s, "ivan")
	q1 := addUnowned(t, s, "Q1")
	dir := t.TempDir()
	writeQuestionsFile(t, dir, "teacher_ivan_mid.json", "Q1")
	for run := 1; run <= 2; run++ {
		if err := backfillQuestionOwners(s, dir); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if got := ownerOf(t, s, q1); got != ivan {
			t.Errorf("run %d: owner = %d, want %d", run, got, ivan)
		}
	}
	// A file appearing later does not take over an owned question.
	olga := addTeacher(t, s, "olga")
	writeQuestionsFile(t, dir, "teacher_olga_x.json", "Q1")
	if err := backfillQuestionOwners(s, dir); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, s, q1); got != ivan || got == olga {
		t.Errorf("owned question reassigned to %d", got)
	}
}

func TestBackfillMissingDir(t *testing.T) {
	_, s := loadEnv(t)
	admin, _ := s.FirstAdminID()
	q1 := addUnowned(t, s, "Q1")
	if err := backfillQuestionOwners(s, filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
	if got := ownerOf(t, s, q1); got != admin {
		t.Errorf("owner = %d, want admin %d", got, admin)
	}
}

func TestBackfillIgnoresStudents(t *testing.T) {
	_, s := loadEnv(t)
	ivan := addTeacher(t, s, "ivan")
	if _, err := s.CreateUser(model.User{Username: "ivan_mid", DisplayName: "s", PasswordHash: "x", Role: model.UserRoleStudent, Active: true}); err != nil {
		t.Fatal(err)
	}
	q1 := addUnowned(t, s, "Q1")
	dir := t.TempDir()
	writeQuestionsFile(t, dir, "teacher_ivan_mid_term.json", "Q1")
	if err := backfillQuestionOwners(s, dir); err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(t, s, q1); got != ivan {
		t.Errorf("owner = %d, want teacher ivan %d (not the student)", got, ivan)
	}
}
