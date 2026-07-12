# PR #33 Follow-Up Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or execute tasks inline. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the fixes defined in `docs/superpowers/specs/2026-07-12-pr33-followup-design.md` and land them on branch `bug/pr33-followup`.

**Architecture:** Move JSON schema compilation to handler construction, add two store helpers for safe question update/delete, refactor the teacher upload handler to use them, and clean up linter warnings.

**Tech Stack:** Go, `github.com/santhosh-tekuri/jsonschema/v5`, SQLite, templ.

---

## Task 1: Compile question schema once in `NewHandler`

**Files:**
- Modify: `internal/handler/handler.go`
- Modify: `internal/handler/teacher.go`

**Step 1: Add schema field and compile helper**

In `internal/handler/handler.go`, change the imports and struct:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pavelanni/examiner/internal/handler/views"
	"github.com/pavelanni/examiner/internal/llm"
	"github.com/pavelanni/examiner/internal/model"
	"github.com/pavelanni/examiner/internal/store"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

type Handler struct {
	store          *store.Store
	llm            *llm.Client
	config         model.ExamConfig
	questionSchema *jsonschema.Schema
}
```

Replace `New` with:

```go
func New(s *store.Store, l *llm.Client, cfg model.ExamConfig) (*Handler, error) {
	schema, err := compileQuestionSchema()
	if err != nil {
		return nil, fmt.Errorf("compile question schema: %w", err)
	}
	return &Handler{store: s, llm: l, config: cfg, questionSchema: schema}, nil
}

func compileQuestionSchema() (*jsonschema.Schema, error) {
	absSchema, err := filepath.Abs("schema/question_schema.json")
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	f, err := os.Open(absSchema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	schemaURL := "file://" + filepath.ToSlash(absSchema)
	if err := compiler.AddResource(schemaURL, f); err != nil {
		return nil, err
	}
	return compiler.Compile(schemaURL)
}
```

**Step 2: Use the cached schema in the upload handler**

In `internal/handler/teacher.go`, remove the local schema loading block (lines 148-177) and replace it with:

```go
	// Validate the JSON structure against the compiled schema.
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.questionSchema.Validate(v); err != nil {
		http.Error(w, "schema validation failed: "+err.Error(), http.StatusBadRequest)
		return
	}
```

Remove the now-unused imports `os`, `path/filepath` from `teacher.go` if they are no longer used elsewhere.

**Step 3: Run tests**

```bash
templ generate && go test ./...
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/handler/handler.go internal/handler/teacher.go
git commit -s -m "refactor: compile question schema once in NewHandler" -m "Assisted-By: Claude (Anthropic AI) <noreply@anthropic.com>"
```

---

## Task 2: Add `UpdateQuestionByCourseAndText` store helper

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

**Step 1: Implement the helper**

Add to `internal/store/store.go`:

```go
// UpdateQuestionByCourseAndText updates a question matched by course_id and text.
// It returns sql.ErrNoRows if no matching row exists.
func (s *Store) UpdateQuestionByCourseAndText(q model.Question) error {
	res, err := s.db.Exec(
		`UPDATE questions
		 SET difficulty = ?, topic = ?, rubric = ?, model_answer = ?, max_points = ?
		 WHERE course_id = ? AND text = ?`,
		q.Difficulty, q.Topic, q.Rubric, q.ModelAnswer, q.MaxPoints, q.CourseID, q.Text,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
```

**Step 2: Add a test**

Append to `internal/store/store_test.go`:

```go
func TestUpdateQuestionByCourseAndText(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close() }()

	q := model.Question{
		CourseID:    1,
		Text:        "update me",
		Difficulty:  "easy",
		Topic:       "go",
		Rubric:      "original",
		ModelAnswer: "original answer",
		MaxPoints:   5,
	}
	id, err := s.InsertQuestion(q)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected inserted id")
	}

	q.Rubric = "updated"
	q.MaxPoints = 10
	if err := s.UpdateQuestionByCourseAndText(q); err != nil {
		t.Fatalf("update: %v", err)
	}

	updated, err := s.GetQuestion(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Rubric != "updated" || updated.MaxPoints != 10 {
		t.Fatalf("unexpected updated values: %+v", updated)
	}

	missing := model.Question{CourseID: 1, Text: "missing", Difficulty: "easy", Topic: "x"}
	if err := s.UpdateQuestionByCourseAndText(missing); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}
```

Add `errors` to the test file imports if not present.

**Step 3: Run tests**

```bash
go test ./internal/store/... -v -run TestUpdateQuestionByCourseAndText
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -s -m "feat: add UpdateQuestionByCourseAndText store helper" -m "Assisted-By: Claude (Anthropic AI) <noreply@anthropic.com>"
```

---

## Task 3: Add `DeleteUnusedQuestionsByTexts` store helper

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

**Step 1: Implement the helper**

Add to `internal/store/store.go` after `UpdateQuestionByCourseAndText`:

```go
// DeleteUnusedQuestionsByTexts deletes questions whose text is in oldTexts but not in keepTexts
// and that are not referenced by any question_thread.
func (s *Store) DeleteUnusedQuestionsByTexts(courseID int, oldTexts, keepTexts []string) error {
	if len(oldTexts) == 0 {
		return nil
	}

	args := []any{courseID}
	args = append(args, stringsToAny(oldTexts)...)

	var notInClause string
	if len(keepTexts) > 0 {
		notInClause = "AND text NOT IN (" + placeholders(len(keepTexts)) + ")"
		args = append(args, stringsToAny(keepTexts)...)
	}

	query := `DELETE FROM questions
		WHERE course_id = ?
		  AND text IN (` + placeholders(len(oldTexts)) + `)
		  ` + notInClause + `
		  AND NOT EXISTS (
		      SELECT 1 FROM question_threads WHERE question_threads.question_id = questions.id
		  )`

	_, err := s.db.Exec(query, args...)
	return err
}

func stringsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
```

**Step 2: Add a test**

Append to `internal/store/store_test.go`:

```go
func TestDeleteUnusedQuestionsByTexts(t *testing.T) {
	s := newTestStore(t)
	defer func() { _ = s.Close() }()

	insert := func(text string) int64 {
		id, err := s.InsertQuestion(model.Question{
			CourseID:    1,
			Text:        text,
			Difficulty:  "easy",
			Topic:       "go",
			Rubric:      "r",
			ModelAnswer: "a",
			MaxPoints:   1,
		})
		if err != nil {
			t.Fatalf("insert %q: %v", text, err)
		}
		return id
	}

	idKept := insert("kept")
	idRemoved := insert("removed")
	idReferenced := insert("referenced")

	// Create a session that references idReferenced so it cannot be deleted.
	bpID, err := s.CreateBlueprint(model.ExamBlueprint{Name: "test", TimeLimit: 10, MaxFollowups: 0})
	if err != nil {
		t.Fatalf("create blueprint: %v", err)
	}
	_, err = s.CreateSession(bpID, 1, []int64{idReferenced})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := s.DeleteUnusedQuestionsByTexts(1, []string{"kept", "removed", "referenced"}, []string{"kept"}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := s.GetQuestion(idRemoved); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected removed question to be deleted, got err=%v", err)
	}
	if _, err := s.GetQuestion(idKept); err != nil {
		t.Fatalf("expected kept question to remain: %v", err)
	}
	if _, err := s.GetQuestion(idReferenced); err != nil {
		t.Fatalf("expected referenced question to remain: %v", err)
	}
}
```

**Step 3: Run tests**

```bash
go test ./internal/store/... -v -run TestDeleteUnusedQuestionsByTexts
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -s -m "feat: add DeleteUnusedQuestionsByTexts store helper" -m "Assisted-By: Claude (Anthropic AI) <noreply@anthropic.com>"
```

---

## Task 4: Refactor upload handler for safe edit/replace

**Files:**
- Modify: `internal/handler/teacher.go`

**Step 1: Replace delete + insert loop with upsert + safe delete**

In `handleTeacherUpload`, after writing the file and after parsing `oldQuestions`, replace the old delete/insert block (lines 255-282) with:

```go
	newTexts := make(map[string]struct{}, len(questions))
	for _, qi := range questions {
		q := model.Question{
			// TODO: derive course ID from context/config when multi-course support lands.
			CourseID:    1,
			Text:        qi.Text,
			Difficulty:  qi.Difficulty,
			Topic:       qi.Topic,
			Rubric:      qi.Rubric,
			ModelAnswer: qi.ModelAnswer,
			MaxPoints:   qi.MaxPoints,
		}
		if err := h.store.UpdateQuestionByCourseAndText(q); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				slog.Error("failed to update question", "error", err)
				http.Error(w, "failed to update question: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if _, err := h.store.InsertQuestion(q); err != nil {
				slog.Error("failed to insert question", "error", err)
				http.Error(w, "failed to insert question: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		newTexts[q.Text] = struct{}{}
	}

	if len(oldQuestions) > 0 {
		oldTexts := make([]string, 0, len(oldQuestions))
		for _, q := range oldQuestions {
			oldTexts = append(oldTexts, q.Text)
		}
		keepTexts := make([]string, 0, len(newTexts))
		for text := range newTexts {
			keepTexts = append(keepTexts, text)
		}
		if err := h.store.DeleteUnusedQuestionsByTexts(1, oldTexts, keepTexts); err != nil {
			slog.Error("failed to remove old questions", "error", err)
			http.Error(w, "failed to update questions", http.StatusInternalServerError)
			return
		}
	}
```

Add `errors` to the imports in `teacher.go` if not present.

**Step 2: Remove orphaned old file on rename**

After the `os.WriteFile` call succeeds, add:

```go
	if editingFile != "" && safeName != filepath.Base(editingFile) {
		oldPath := filepath.Join("questions", filepath.Base(editingFile))
		if err := os.Remove(oldPath); err != nil {
			slog.Warn("failed to remove old question file", "path", oldPath, "error", err)
		}
	}
```

**Step 3: Delete unused store helper and fix Close warnings**

In `internal/store/store.go`, remove the now-unused `DeleteQuestionsByTexts` helper added by PR #33.

In `internal/handler/teacher.go`, change:
- `defer file.Close()` to `defer func() { _ = file.Close() }()`

**Step 4: Run tests**

```bash
templ generate && go test ./...
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/handler/teacher.go internal/store/store.go
git commit -s -m "fix: safe edit/replace flow for teacher question files" -m "Avoid duplicating DB rows, skip referenced questions, and remove orphaned files on rename." -m "Assisted-By: Claude (Anthropic AI) <noreply@anthropic.com>"
```

---

## Task 5: Quality gates and final verification

**Step 1: Run full quality gates**

```bash
templ generate
go test ./...
go vet ./...
golangci-lint run ./...
```

Expected:
- `go test ./...`: PASS
- `go vet ./...`: no output / exit 0
- `golangci-lint run ./...`: zero new issues in `internal/handler/teacher.go` or `internal/store/store.go`

**Step 2: Push branch**

```bash
git push -u origin bug/pr33-followup
```

**Step 3: Open PR and credit original author**

```bash
gh pr create --base main --head bug/pr33-followup --title "fix: address remaining PR #33 review findings" --body "This branch builds on PR #33 and resolves the remaining CodeRabbit items and newly identified risks.

Co-Authored-By: Brodyachi <maks.zabrodin.04@gmail.com>"
```

Then mark the PR ready for review and, once CI passes, squash merge with the `Co-Authored-By` trailer in the squash commit message.
