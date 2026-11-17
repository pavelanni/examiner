# Question Validation and Atomic Import Design (#42)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:writing-plans to create the implementation plan after this spec is approved.

**Goal:** One set of question rules (FR1-01) and one import pipeline (FR1-02) for every way questions enter the system. A file with any error writes nothing and reports every error with the question number and field.

**Architecture:** A new package `internal/questionimport` owns decoding, validation, duplicate planning and the write algorithm. The store only provides transaction-scoped primitives. The admin import, the teacher constructor/upload and the `--questions` startup load all call the same chain.

**Tech Stack:** Go 1.25, `encoding/json`, modernc SQLite (`_txlock=immediate`), templ, go-i18n. `santhosh-tekuri/jsonschema` stays, but only in tests.

---

## Background

Issue #42 lists four entry points with different checks:

| Entry point | Validation today | Atomicity today |
| --- | --- | --- |
| Admin `POST /admin/questions` | `json.Unmarshal` only, array format only | one insert per question, no transaction |
| Teacher `POST /teacher/tests` | weak JSON Schema (`text`, `max_points` required; `max_points >= 0`) | file written to `questions/` before the DB; DB writes not in a transaction |
| Startup `--questions` (`loadQuestions` in `cmd/examiner/main.go`) | none | none |
| Constructor UI | `min="1"` on points only | n/a |

Errors are returned as `text/plain` with raw validator text and no translation.

`compileQuestionSchema` builds a `file://D:/...` URL on Windows, which parses `D:` as the host. On Windows the server cannot start and no handler test can construct a `Handler`.

### Relation to PR #73 (#43)

PR #73 adds `model.NormalizeQuestionText`, the `text_norm` column, `FindQuestionByTopicAndTextNorm`, `UpdateQuestion`, and duplicate checks in the admin and teacher upload loops. This work is built **on top of the PR #73 branch** and merges after it. The duplicate rules of #73 move into `questionimport.Apply` unchanged in behavior, with messages moved to i18n.

#43 defines duplicates as **warnings**: the duplicate is skipped and the import goes on. Atomicity therefore applies to validation errors and write failures, not to duplicates.

### Known identity mismatch (out of scope)

The DB enforces `UNIQUE (course_id, text)` (`idx_questions_course_text`). #73 deduplicates by `(topic, text_norm)`. A question whose exact text exists in another topic passes #73's check and is then silently dropped by `INSERT OR IGNORE`. This design detects that case during planning and reports it as a duplicate warning. Fixing the identity model is #74 item 1.

## Design

### 1. Package `internal/questionimport`

It depends on `internal/model` only. It does not import `handler` or `store`.

```
bytes ──Decode──▶ []Draft ──Validate──▶ []FieldError     any error → stop, nothing written
                     │
                     └── store.InQuestionTx(func(tx) { Apply(tx, drafts, opts) })
                                                │ commit
                     teacher only: rename temp file into place, remove old file
```

#### Types

```go
// Draft is one decoded question. Values that could be read are kept even
// when another field has the wrong type, so the constructor can show them again.
type Draft struct {
    Number int // 1-based position in the file
    model.QuestionImport
}

// FieldError describes one problem. Question == 0 means the file as a whole.
type FieldError struct {
    Question int
    Field    string         // JSON field name; empty for file-level errors
    Code     string         // i18n message ID, e.g. "QImportErrRequired"
    Params   map[string]any // template data for the message
}

type Mode int

const (
    ModeSkipExisting   Mode = iota // admin import and startup: DB duplicates are skipped
    ModeUpdateExisting             // teacher save: DB duplicates are updated in place
)

type FileRecord struct{ Name, Hash string }

type Options struct {
    Mode     Mode
    CourseID int64
    OldTexts []string    // teacher edit: questions of the file being replaced
    File     *FileRecord // recorded in imported_files inside the transaction
}

type Warning struct {
    Question int
    Code     string // i18n message ID
    Params   map[string]any
}

type Result struct {
    Inserted, Updated, Skipped int
    Warnings                   []Warning
}

// Tx is the transaction-scoped store surface Apply needs.
type Tx interface {
    FindByTopicAndTextNorm(topic, textNorm string) (*model.Question, error)
    FindByCourseAndText(courseID int64, text string) (*model.Question, error)
    Insert(q model.Question) (int64, error)
    Update(q model.Question) error // by ID; sql.ErrNoRows if missing
    DeleteUnusedByTexts(courseID int64, oldTexts, keepTexts []string) error
    SetImportedFileHash(name, hash string) error
}
```

#### Functions

```go
func Decode(data []byte) ([]Draft, []FieldError)
func Validate(drafts []Draft) []FieldError
func Apply(tx Tx, drafts []Draft, opts Options) (Result, error)
func Localize(ctx context.Context, errs []FieldError) []string // rendered messages
func LocalizeWarnings(ctx context.Context, ws []Warning) []string
```

`Localize` uses `i18n.Td` with the request context. At startup `main.go` builds a context with a localizer for the configured `lang`.

### 2. Decoding (`Decode`)

- **Top level:** either a JSON array of questions or an object `{"questions": [...]}` with optional `test_name`, `test_description`, `default_topic`. The three optional fields are accepted and ignored, as today. Any other key in the wrapper is a file-level error. Any other top-level JSON type is a file-level error (`QImportErrTopLevel`).
- **Invalid JSON:** a file-level error `QImportErrInvalidJSON` with line and column computed from `*json.SyntaxError.Offset`.
- **No questions:** a file-level error `QImportErrNoQuestions`.
- **Each question** is decoded into `map[string]json.RawMessage`. A question that is not a JSON object gets `QImportErrNotObject` for that question number. Each known field is decoded separately:
  - `text`, `topic`, `rubric`, `model_answer`, `difficulty` must be JSON strings, else `QImportErrNotString`.
  - `max_points` is decoded with `json.Decoder.UseNumber()`. The literal must parse with `strconv.ParseInt(s, 10, 64)`, so `"5"` (string, `QImportErrNotInteger`), `2.5`, `10.0`, `1e1` (`QImportErrNotInteger`) are rejected.
  - Any other key: `QImportErrUnknownField` with the key name in `Field`.
- Decoding errors for one question do not stop decoding of the others. `Decode` returns all drafts and all errors.

### 3. Validation rules (`Validate`)

Applied after `strings.TrimSpace` on string fields. The trimmed values are what gets stored, because `topic` is part of the duplicate key.

| Field | Rule | Code |
| --- | --- | --- |
| `text`, `topic`, `rubric`, `model_answer` | present and non-empty | `QImportErrRequired` |
| `difficulty` | present and exactly `easy`, `medium` or `hard` | `QImportErrRequired` / `QImportErrDifficulty` |
| `max_points` | present, integer, `> 0`, no upper bound | `QImportErrRequired` / `QImportErrPositive` |

A field that already has a decoding error is not validated again, so each field has at most one error.

**Ordering:** errors are sorted by question number, then by field in the order `text, difficulty, topic, rubric, model_answer, max_points`, then unknown fields by name. File-level errors come first.

**Callers** run `Decode` and `Validate` and stop if the combined list is non-empty.

### 4. JSON Schema

`schema/question_schema.json` stays as the published contract for people who write files by hand. It is no longer loaded at runtime.

Changes: all six question fields `required`; string fields get `"minLength": 1, "pattern": "\\S"`; `max_points` gets `"minimum": 1`.

Shared fixtures in `internal/questionimport/testdata/valid/*.json` and `testdata/invalid/*.json`. A test compiles the schema with `jsonschema.NewCompiler()` + `AddResource("mem://question_schema.json", …)` (no `file://` URL, so it works on Windows) and checks that the schema and `Decode`+`Validate` agree on valid/invalid for every fixture.

`compileQuestionSchema`, `Handler.questionSchema` and the runtime dependency on the working directory are removed.

### 5. Store: transaction primitives

```go
// InQuestionTx runs fn in one transaction. _txlock=immediate in the DSN makes
// BEGIN take the write lock, so lookups inside fn cannot race with other writers.
func (s *Store) InQuestionTx(ctx context.Context, fn func(*QuestionTx) error) error

type QuestionTx struct{ tx *sql.Tx }
```

`*QuestionTx` implements `questionimport.Tx`:

- `FindByTopicAndTextNorm`: same query as #73's `FindQuestionByTopicAndTextNorm`, on the transaction.
- `FindByCourseAndText`: exact match on `(course_id, text)`.
- `Insert`: plain `INSERT` (no `OR IGNORE`). A constraint violation is an error and rolls the whole import back.
- `Update`: by ID, sets `text_norm`. `sql.ErrNoRows` if no row matched.
- `DeleteUnusedByTexts`: the current `DeleteUnusedQuestionsByTexts` query, on the transaction.
- `SetImportedFileHash`: the current upsert, on the transaction.

`fn` returning an error, or any statement failing, rolls back. `UpdateQuestionByCourseAndText` and `DeleteUnusedQuestionsByTexts` are deleted together with their tests (#74 item 5). `InsertQuestion`, `FindQuestionByTopicAndTextNorm` and `UpdateQuestion` stay for their other callers.

### 6. `Apply` algorithm

For each draft, in file order:

1. `norm := model.NormalizeQuestionText(text)`; key `topic + "\x00" + norm`.
2. Key seen earlier in this file → skip, warning `QImportWarnDupInFile` {N, PrevN, Topic}.
3. `FindByTopicAndTextNorm(topic, norm)` finds a row:
   - `ModeSkipExisting` → skip, warning `QImportWarnDupInDB` {N, ID, Topic}.
   - `ModeUpdateExisting` → `Update` with the existing ID, `Updated++`.
4. Else `FindByCourseAndText(courseID, text)` finds a row (exact text in another topic) → skip, warning `QImportWarnDupOtherTopic` {N, ID, Topic: existing topic}.
5. Else `Insert`, `Inserted++`.

Then, if `OldTexts` is non-empty, `DeleteUnusedByTexts(courseID, OldTexts, keepTexts)`, where `keepTexts` are the texts of all drafts. Then, if `File != nil`, `SetImportedFileHash`.

`CourseID` stays `1` at every call site, with the comment `// TODO: derive course ID from context/config when multi-course support lands.` restored in `teacher.go` (#74 item 6b).

### 7. Entry points

| Entry point | Mode | `File` | On validation errors |
| --- | --- | --- | --- |
| Admin `POST /admin/questions` | `ModeSkipExisting` | upload filename + hash | 400, admin page with error list |
| Teacher `POST /teacher/tests` | `ModeUpdateExisting`, `OldTexts` from the edited file | saved filename + hash | 400, constructor re-rendered with the submitted questions and error list |
| `loadQuestions` (startup and manifest mode) | `ModeSkipExisting` | path + hash | returns an error listing every error; startup stops |

- **Admin:** the "file already imported" hash check runs before decoding, as today.
- **Startup:** every file is decoded and validated on every start, including files whose hash is already recorded. The existing behavior for unchanged files (skip) and changed files (warn and skip) then applies. Validation failures are fatal.
- **Teacher file write order:**
  1. Decode and validate. Errors → 400, nothing touched.
  2. Write the payload to `questions/.tmp-<random>.json` (`os.CreateTemp` in `questions/`), check `Close()`.
  3. `InQuestionTx(Apply)`. Error → remove the temp file, 500.
  4. After commit: `os.Rename(tmp, outPath)`. If the edit renamed the file, remove the old file (failure is a warning in the log, as today).
  5. A `Rename` failure after commit is logged as an error and returns 500. The DB is consistent, and saving again repairs the file.
- **Non-validation errors** return 500 with a generic localized message (`QImportErrInternal`). Details go to `slog` only. No `err.Error()` in responses on these paths (#74 item 4).
- **Warnings** appear on the success path: the admin page lists them; the teacher redirect to `/teacher/profile` carries none (the profile page has no flash area), so teacher warnings are logged. Showing them to the teacher is left for #43's constructor work.
- `parseQuestionsFromJSON` in `teacher.go` (used to read the edited file's old questions and to prefill the editor) is replaced by `Decode`, ignoring errors for the prefill and old-text cases.

### 8. UI

- **Admin page:** `AdminQuestionsPage` gets `errs []string` and `warnings []string`, rendered as lists. The single flash line stays for the success summary ("Imported N, updated M, skipped K").
- **Constructor:** `TeacherCreateTestPage` gets `errs []string`. On 400 the page is rendered with the drafts as `preloaded-data` and the submitted `custom_filename` and `editing_file`. Draft numbers match the builder's `#N` labels because only submitted questions are preloaded. A field with a decoding error is shown empty, or `0` for `max_points`.
- **Client-side checks** in `assembleAndSubmit`: a started block (any of text/topic filled) must have all of text, topic, rubric, model answer and points; points must be an integer `>= 1`. Untouched blocks are still ignored. Alert texts come from `data-` attributes filled with `t(ctx, …)`. The server remains the source of truth.

### 9. i18n

New message IDs in `active.en.json` and `active.ru.json`:

`QImportErrInvalidJSON`, `QImportErrTopLevel`, `QImportErrNoQuestions`, `QImportErrNotObject`, `QImportErrNotString`, `QImportErrNotInteger`, `QImportErrRequired`, `QImportErrDifficulty`, `QImportErrPositive`, `QImportErrUnknownField`, `QImportErrInternal`, `QImportWarnDupInFile`, `QImportWarnDupInDB`, `QImportWarnDupOtherTopic`, `QImportSummary`, plus the constructor alert texts.

Question-level messages are prefixed with the question and field, e.g. «Вопрос №3, поле `max_points`: должно быть целым числом больше 0» / "Question 3, field `max_points`: must be an integer greater than 0".

### 10. Testing

- `questionimport`:
  - `Decode`, table-driven: invalid JSON (line/column), wrong top level, unknown wrapper key, empty list, non-object question, non-string field, `max_points` as `"5"`, `2.5`, `10.0`, `1e1`, unknown field; several errors in several questions reported together.
  - `Validate`, table-driven: one case per rule, trimming, ordering.
  - `Apply` with a fake `Tx`: duplicate in file, DB duplicate in both modes, exact text in another topic, `OldTexts` deletion with `keepTexts`, file hash recorded, error from `Tx` propagated.
  - Schema-vs-Go agreement on the shared fixtures.
- `store`: `InQuestionTx` commits on success; rolls back when `fn` returns an error and when `Insert` violates `idx_questions_course_text`.
- `handler` (acceptance test from #42), for admin and teacher: a file of 10 questions with an error in the 7th returns 400; the body contains the question number 7 and the field; the question count and the contents of `questions/` are unchanged. Teacher success: the file is in place and no `.tmp-*` remains. Tests use `t.Chdir`.
- `cmd/examiner`: `loadQuestions` with an invalid file returns an error containing every error.

### 11. Housekeeping in the same PR

- `.gitattributes`: `*.golden text eol=lf`, so the `internal/llm` golden tests pass on Windows checkouts with `core.autocrlf=true`.
- Fix the outdated dedup comment in `handleStartExam` (#74 item 6a).
- README: field rules and error reporting, next to the existing JSON format section.

## Out of scope

- Identity model `(course_id, text)` vs `(topic, text_norm)` (#74 item 1).
- Question ownership and permissions (#44).
- Editing a question by `id` in the constructor (#43).
- Showing duplicate warnings to the teacher in the constructor (#43).

## Rollout

- The PR targets `main`, states that it depends on #73, and is rebased after #73 merges.
- Before deploying: copy `~/examiner/questions-en.json` and `~/examiner/questions-ru.json` from `examiner-01`, start a local server with them against a temporary DB, and fix any reported errors on the host first. Startup validation is strict and fatal.
