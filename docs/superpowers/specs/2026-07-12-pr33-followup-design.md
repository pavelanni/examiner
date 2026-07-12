# PR #33 Follow-Up Design

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:writing-plans to create the implementation plan after this spec is approved.

**Goal:** Address the remaining CodeRabbit review items and newly identified risks in the teacher question constructor feature introduced by PR #33, then land the work on a new branch that preserves attribution to the original author.

**Architecture:** Keep changes localized to the teacher upload/edit handler and the SQLite store. Replace the fragile "delete all old questions by text then re-insert" edit flow with an update-in-place strategy, compile the JSON schema once, fix linter warnings, and clean up orphaned files on rename. The new branch is based on the current PR #33 head so the original commits remain in history.

**Tech Stack:** Go, `github.com/santhosh-tekuri/jsonschema/v5`, SQLite, templ.

---

## Background

PR #33 adds a teacher-facing question constructor and editor. CodeRabbit and a manual review identified several items that were not fully resolved in the latest commit (`6a9a3e0`):

- JSON schema is compiled on every upload request.
- `CourseID` is hardcoded to `1` without explanation.
- Editing a question file deletes old questions by text and re-inserts them, which can 500 on FK violations and delete identical questions owned by other teachers/files.
- Renaming a file during edit leaves the old JSON file on disk.
- `golangci-lint` reports unchecked `Close()` errors in the new handler code.

This follow-up branch fixes those issues before the feature is merged to `main`.

## Design

### 1. Branching strategy

Create branch `bug/pr33-followup` from the current PR #33 head (`6a9a3e0`).
The final squash-merge commit message to `main` will include:

```text
Co-Authored-By: Brodyachi <maks.zabrodin.04@gmail.com>
```

This preserves credit for the original author even after squash merging.

### 2. Compile JSON schema once

Move schema loading/compilation out of `handleTeacherUpload`.

Options considered:
- Package-level `var` with `sync.Once`: simple but panics on init failure.
- Initialize in `NewHandler` and store a compiled `*jsonschema.Schema` on `Handler`: cleaner error handling and testability.

**Chosen:** initialize in `NewHandler`. If the schema cannot be loaded, `NewHandler` returns an error and the application fails fast on startup.

### 3. Hardcoded `CourseID`

Keep `CourseID: 1` for now because the application has no multi-course support yet.
Add an explicit TODO comment above the constant usage:

```go
// TODO: derive course ID from context/config when multi-course support lands.
CourseID: 1,
```

### 4. Safe edit/replace flow

Replace the current delete-by-text + insert flow with:

1. Parse incoming questions.
2. For each question, attempt to update the existing row by `(course_id, text)`.
3. If no row was updated, insert the question as new.
4. After all questions are upserted, delete any rows for `(course_id, text)` that:
   - were present in the old file,
   - are **not** present in the new file,
   - and are **not** referenced by any `question_thread`.

This avoids:
- Duplicating rows on each edit.
- FK violations when a question is part of an exam session.
- Accidentally deleting another teacher's identical question.

New store helpers:

```go
// UpdateQuestionByCourseAndText updates a question matched by course_id and text.
func (s *Store) UpdateQuestionByCourseAndText(q model.Question) error

// DeleteUnusedQuestionsByTexts removes questions whose text is in oldTexts but not in keepTexts
// and that are not referenced by any question_thread.
func (s *Store) DeleteUnusedQuestionsByTexts(courseID int, oldTexts, keepTexts []string) error
```

### 5. Remove orphaned old file on rename

In the edit flow, if `customFilename` produces a filename different from `editingFile`, delete the old file after the new file is written and questions are persisted. Deletion is best-effort; log errors but do not fail the request.

### 6. Fix unchecked Close errors

In `handleTeacherUpload`, explicitly ignore or log the return values of:
- `file.Close()` for the uploaded file.
- `f.Close()` for the schema file.

Use `_ = file.Close()` style so `errcheck` passes.

## Files to modify

- `internal/handler/teacher.go`
- `internal/store/store.go`
- `internal/handler/handler.go` (if schema init happens in `NewHandler`)
- `internal/store/store_test.go` (add tests for new helpers)

## Testing

- `templ generate`
- `go test ./...`
- `go vet ./...`
- `golangci-lint run ./...` — must report zero new issues in changed files.

## Acceptance criteria

- `handleTeacherUpload` does not compile the JSON schema on each request.
- Editing a question file does not duplicate questions.
- Editing a question file used in an existing exam session does not 500.
- Renaming a file during edit removes the old file from disk.
- `golangci-lint` reports no new warnings in `internal/handler/teacher.go` or `internal/store/store.go`.
- Original PR author is credited in the final merge commit.
