# Question Validation and Atomic Import Implementation Plan (#42)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One rule set and one transactional import pipeline for the admin import, the teacher constructor/upload and the `--questions` startup load; a file with any error writes nothing and reports every error with question number and field.

**Architecture:** New package `internal/questionimport` (decode, validate, apply, localize) that depends only on `internal/model` and `internal/i18n`. The store exposes `InQuestionTx` with a `*QuestionTx` that satisfies `questionimport.Tx`. Handlers and `cmd/examiner` call `Parse` → `InQuestionTx(Apply)`.

**Tech Stack:** Go 1.25, `encoding/json`, modernc SQLite (`_txlock=immediate` already in DSN), templ, go-i18n, `santhosh-tekuri/jsonschema/v5` (tests only after this change).

**Spec:** `docs/superpowers/specs/2026-10-28-question-validation-import-design.md`

## Global Constraints

- Branch `feature/issue-42-question-validation`, based on PR #73's branch. PR #73's `model.NormalizeQuestionText`, `text_norm` column, `FindQuestionByTopicAndTextNorm`, `UpdateQuestion` already exist here.
- Run `templ generate` before building or testing `internal/handler` (generated `*_templ.go` files are not committed).
- Lint: `golangci-lint run` (v2, `.golangci.yml`). Check `Close()` errors on files you write; `_ = x.Close()` is fine for read-only resources.
- Commit trailer: `Assisted-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never `Co-Authored-By`.
- `CourseID` is `1` at every call site.
- Field order for errors: `text, difficulty, topic, rubric, model_answer, max_points`, then unknown fields by name; file-level errors (`Question == 0`) first.
- Valid difficulties, exact match: `easy`, `medium`, `hard`. `max_points`: JSON integer literal (`strconv.ParseInt(s, 10, 64)`), `> 0`, no upper bound.
- No `err.Error()` in HTTP responses on the import paths; log details with `slog`, respond with `QImportErrInternal`.
- Inside `InQuestionTx`, use only the `*QuestionTx`; never `s.db`. With `:memory:` a second pool connection is a different, empty database.

## Review Focus

- A file saved by a Windows editor starts with a UTF-8 BOM; it must import like the same file without one (test in Task 1).
- `"rubric": null` (and `null` for any field) must read as "required", not as a type error (test in Task 1).
- Admin uploads an invalid file, fixes it, uploads again under the same name: the second upload is validated and imported, not rejected as "already imported", because the hash is recorded only on success (test in Task 8).
- Teacher edits an existing file and submits an invalid version: the old file, its name and its DB rows stay exactly as they were (test in Task 9).
- A `--questions` file in the wrapper format `{"questions": [...]}` loads at startup (previously only arrays were accepted there) (test in Task 7).

---

### Task 1: Package skeleton and `Decode`

**Files:**
- Create: `.gitattributes`
- Create: `internal/questionimport/questionimport.go` (types)
- Create: `internal/questionimport/decode.go`
- Test: `internal/questionimport/decode_test.go`

**Interfaces:**
- Produces:
  ```go
  type Draft struct {
      Number int // 1-based
      model.QuestionImport
      present map[string]bool // JSON keys that were present and not null
      bad     map[string]bool // fields that already have a decode error
  }
  type FieldError struct {
      Question int            // 0 = file level
      Field    string
      Code     string         // i18n message ID
      Params   map[string]any
  }
  func Decode(data []byte) ([]Draft, []FieldError)
  // Codes used: QImportErrInvalidJSON {Line, Col}, QImportErrTopLevel, QImportErrUnknownTopField {Field},
  // QImportErrNoQuestions, QImportErrNotObject, QImportErrNotString, QImportErrNotInteger, QImportErrUnknownField
  ```

- [ ] **Step 1: Fix golden files on Windows checkouts**

Create `.gitattributes` with `*.golden text eol=lf`, then `rm internal/llm/testdata/*.golden && git checkout -- internal/llm/testdata`.
Run: `go test ./internal/llm/ -count=1` — Expected: `ok`.

- [ ] **Step 2: Write failing table-driven tests in `decode_test.go`**

`TestDecode` cases (input → expected drafts count, errors as `[]FieldError{Question, Field, Code}` ignoring Params unless noted):
- `[{valid q}]` → 1 draft, `Number == 1`, no errors; string values trimmed (`"  Q  "` → `"Q"`).
- `{"test_name":"x","default_topic":"t","questions":[{valid}]}` → 1 draft, no errors.
- `{"questions":[...],"extra":1}` → `{0, "extra", QImportErrUnknownTopField}`.
- `"str"` and `42` → `{0, "", QImportErrTopLevel}`.
- `[]` and `{"questions":[]}` → `{0, "", QImportErrNoQuestions}`.
- `"[\n{\"text\": }"` → `{0, "", QImportErrInvalidJSON}` with `Params["Line"] == 2`.
- `[1]` → `{1, "", QImportErrNotObject}`, 1 draft.
- `max_points` as `"5"`, `2.5`, `10.0`, `1e1` → `{1, "max_points", QImportErrNotInteger}` each.
- `"text": 5` → `{1, "text", QImportErrNotString}`.
- `"foo": 1` → `{1, "foo", QImportErrUnknownField}`.
- Errors in questions 2 and 4 of a 4-question file are both reported.
- `TestDecodeBOM`: `"\xEF\xBB\xBF" + validArray` decodes with no errors.
- `TestDecodeNull`: `"rubric": null` gives no decode error and the field is not marked present.

- [ ] **Step 3: Run tests, verify they fail**

Run: `go test ./internal/questionimport/ -run TestDecode -v` — Expected: build failure (`undefined: Decode`).

- [ ] **Step 4: Implement types and `Decode`**

Strip a leading BOM. Decode top level into `json.RawMessage`, branch on first non-space byte (`[` array, `{` wrapper with allowed keys `questions, test_name, test_description, default_topic`). Each question: `map[string]json.RawMessage`; string fields via `json.Unmarshal` into `*string` (nil = null → not present); `max_points` via `json.Decoder` with `UseNumber()` then `strconv.ParseInt(n.String(), 10, 64)`. Line/column from `*json.SyntaxError.Offset` by counting `\n` in `data[:offset]`.

- [ ] **Step 5: Run tests, verify they pass**

Run: `go test ./internal/questionimport/ -v` — Expected: PASS.

- [ ] **Step 6: Commit** — `feat(questionimport): strict decoding of question files (#42)` (include `.gitattributes`, golden files if renormalized).

---

### Task 2: `Validate` and `Parse`

**Files:**
- Create: `internal/questionimport/validate.go`
- Test: `internal/questionimport/validate_test.go`

**Interfaces:**
- Consumes: `Draft`, `FieldError`, `Decode` (Task 1).
- Produces:
  ```go
  func Validate(drafts []Draft) []FieldError       // skips fields in Draft.bad
  func Parse(data []byte) ([]Draft, []FieldError)  // Decode + Validate, sorted per Global Constraints
  // Codes: QImportErrRequired, QImportErrDifficulty, QImportErrPositive
  ```

- [ ] **Step 1: Write failing tests**

`TestValidate` (one draft each): empty/whitespace/missing `text`, `topic`, `rubric`, `model_answer` → `QImportErrRequired` on that field; missing `difficulty` → `QImportErrRequired`; `"Easy"` → `QImportErrDifficulty`; missing `max_points` → `QImportErrRequired`; `0` and `-1` → `QImportErrPositive`; fully valid → no errors.
`TestParseOrdering`: question 2 with bad `max_points` and empty `text`, question 1 with unknown field `zz`, file-level wrapper key error → order `{0,…}`, `{1,"zz"}`, `{2,"text"}`, `{2,"max_points"}`.
`TestParseNoDoubleError`: `"max_points": "5"` yields exactly one error for that field (`QImportErrNotInteger`).

- [ ] **Step 2: Run, verify fail** — `go test ./internal/questionimport/ -run 'TestValidate|TestParse' -v` → `undefined: Validate`.

- [ ] **Step 3: Implement `Validate` and `Parse`** — sort with `sort.SliceStable` by `(Question, fieldRank(Field), Field)`, where `fieldRank` uses the Global Constraints order and ranks unknown fields last.

- [ ] **Step 4: Run, verify pass** — `go test ./internal/questionimport/ -v` → PASS.

- [ ] **Step 5: Commit** — `feat(questionimport): validation rules and error ordering (#42)`.

---

### Task 3: i18n messages and `Localize`

**Files:**
- Modify: `internal/i18n/locales/active.en.json`, `internal/i18n/locales/active.ru.json`
- Create: `internal/questionimport/localize.go`
- Test: `internal/questionimport/localize_test.go`

**Interfaces:**
- Consumes: `FieldError` (Task 1); `i18n.Td(ctx, id, data)`.
- Produces:
  ```go
  type Warning struct { Question int; Code string; Params map[string]any }
  func Localize(ctx context.Context, errs []FieldError) []string
  func LocalizeWarnings(ctx context.Context, ws []Warning) []string
  ```
  Rendering: `Question == 0` → `Td(Code, Params)`. Otherwise `msg := Td(Code, Params)`, then `Td("QImportErrAt", {N, Field, Msg})` if `Field != ""`, else `Td("QImportErrAtQuestion", {N, Msg})`. Warnings: `Td(Code, Params)` (Params already include `N`).

- [ ] **Step 1: Add messages** (exact copy, en / ru):

| ID | en | ru |
| --- | --- | --- |
| QImportErrAt | `Question {{.N}}, field {{.Field}}: {{.Msg}}` | `Вопрос №{{.N}}, поле {{.Field}}: {{.Msg}}` |
| QImportErrAtQuestion | `Question {{.N}}: {{.Msg}}` | `Вопрос №{{.N}}: {{.Msg}}` |
| QImportErrInvalidJSON | `Invalid JSON at line {{.Line}}, column {{.Col}}.` | `Некорректный JSON: строка {{.Line}}, столбец {{.Col}}.` |
| QImportErrTopLevel | `The file must contain an array of questions or an object with a "questions" field.` | `Файл должен содержать массив вопросов или объект с полем "questions".` |
| QImportErrUnknownTopField | `Unknown top-level field "{{.Field}}".` | `Неизвестное поле верхнего уровня "{{.Field}}".` |
| QImportErrNoQuestions | `The file contains no questions.` | `В файле нет вопросов.` |
| QImportErrNotObject | `must be a JSON object` | `должен быть JSON-объектом` |
| QImportErrNotString | `must be a string` | `должно быть строкой` |
| QImportErrNotInteger | `must be an integer` | `должно быть целым числом` |
| QImportErrRequired | `is required and must not be empty` | `обязательно и не может быть пустым` |
| QImportErrDifficulty | `must be one of: easy, medium, hard` | `должно быть одним из: easy, medium, hard` |
| QImportErrPositive | `must be an integer greater than 0` | `должно быть целым числом больше 0` |
| QImportErrUnknownField | `unknown field` | `неизвестное поле` |
| QImportErrInternal | `The questions could not be saved because of an internal error. Nothing was changed.` | `Не удалось сохранить вопросы из-за внутренней ошибки. Данные не изменены.` |
| QImportErrorsTitle | `The file was not imported. Fix these errors:` | `Файл не импортирован. Исправьте ошибки:` |
| QImportWarnDupInFile | `Question {{.N}} duplicates question {{.PrevN}} in this file (topic "{{.Topic}}"); skipped.` | `Вопрос №{{.N}} совпадает с вопросом №{{.PrevN}} в этом файле (тема «{{.Topic}}»); пропущен.` |
| QImportWarnDupInDB | `Question {{.N}} matches existing question #{{.ID}} in topic "{{.Topic}}"; skipped.` | `Вопрос №{{.N}} совпадает с существующим вопросом #{{.ID}} в теме «{{.Topic}}»; пропущен.` |
| QImportWarnDupOtherTopic | `Question {{.N}} has the same text as existing question #{{.ID}} in topic "{{.Topic}}"; skipped.` | `Вопрос №{{.N}} совпадает по тексту с существующим вопросом #{{.ID}} в теме «{{.Topic}}»; пропущен.` |
| QImportSummary | `Imported: {{.Inserted}}, updated: {{.Updated}}, skipped: {{.Skipped}}.` | `Добавлено: {{.Inserted}}, обновлено: {{.Updated}}, пропущено: {{.Skipped}}.` |
| QBuilderIncomplete | `Fill in every field of each question you started. Points must be a whole number of at least 1.` | `Заполните все поля каждого начатого вопроса. Баллы — целое число не меньше 1.` |
| QBuilderEmpty | `Add at least one question.` | `Добавьте хотя бы один вопрос.` |

- [ ] **Step 2: Write failing tests** — `TestLocalizeRu`: `FieldError{3, "max_points", "QImportErrPositive", nil}` with a `ru` localizer renders exactly `Вопрос №3, поле max_points: должно быть целым числом больше 0`. `TestLocalizeFileLevel` (en): `QImportErrInvalidJSON {Line:2, Col:5}` → `Invalid JSON at line 2, column 5.`. `TestLocalesComplete`: every `QImport*`/`QBuilder*` ID above exists in both locale files (parse both JSON files, compare ID sets).

- [ ] **Step 3: Run, verify fail** — `go test ./internal/questionimport/ -run TestLocal -v`.
- [ ] **Step 4: Implement `Localize`, `LocalizeWarnings`, `Warning`.**
- [ ] **Step 5: Run, verify pass** — `go test ./internal/questionimport/ ./internal/i18n/ -v`.
- [ ] **Step 6: Commit** — `feat(questionimport): localized error and warning messages (#42)`.

---

### Task 4: JSON Schema aligned with the Go rules

**Files:**
- Modify: `schema/question_schema.json`
- Create: `internal/questionimport/testdata/valid/{array,wrapper}.json`, `internal/questionimport/testdata/invalid/{empty_rubric,missing_topic,points_zero,points_float,points_string,bad_difficulty,unknown_field,whitespace_text,empty_array}.json`
- Test: `internal/questionimport/schema_test.go`

**Interfaces:**
- Consumes: `Parse` (Task 2).

- [ ] **Step 1: Write fixtures and the failing test** — `TestSchemaAgreesWithValidator`: for every fixture, `schemaValid := schema.Validate(v) == nil` and `goValid := len(errs) == 0` from `Parse`; assert `schemaValid == goValid` and that it matches the directory (`valid/` → true). Load the schema from `../../schema/question_schema.json` with `jsonschema.NewCompiler()`, `AddResource("mem://question_schema.json", bytes.NewReader(b))`, `Compile("mem://question_schema.json")`.

- [ ] **Step 2: Run, verify fail** — `go test ./internal/questionimport/ -run TestSchema -v` → mismatches on `empty_rubric`, `missing_topic`, `points_zero`, `whitespace_text`, `empty_array`.

- [ ] **Step 3: Update the schema** — question: `"required"` all six fields; `text, topic, rubric, model_answer`: `{"type":"string","minLength":1,"pattern":"\\S"}`; `difficulty` keeps the enum; `max_points`: `{"type":"integer","minimum":1}`; both `questions` arrays get `"minItems": 1`. Run `go mod tidy` (jsonschema moves from `// indirect` to direct).

- [ ] **Step 4: Run, verify pass** — `go test ./internal/questionimport/ -v`.
- [ ] **Step 5: Commit** — `feat(schema): align question schema with server rules (#42)`.

---

### Task 5: Store transaction primitives

**Files:**
- Create: `internal/store/question_tx.go`
- Test: `internal/store/question_tx_test.go`

`UpdateQuestionByCourseAndText` and `DeleteUnusedQuestionsByTexts` stay until Task 9 removes their last caller, so the tree builds after every task.

**Interfaces:**
- Produces:
  ```go
  func (s *Store) InQuestionTx(ctx context.Context, fn func(*QuestionTx) error) error
  type QuestionTx struct{ tx *sql.Tx }
  func (t *QuestionTx) FindByTopicAndTextNorm(topic, textNorm string) (*model.Question, error) // nil, nil if none
  func (t *QuestionTx) FindByCourseAndText(courseID int64, text string) (*model.Question, error) // nil, nil if none
  func (t *QuestionTx) Insert(q model.Question) (int64, error)  // plain INSERT incl. text_norm (fill from Text if empty)
  func (t *QuestionTx) Update(q model.Question) error           // by ID; sql.ErrNoRows if 0 rows
  func (t *QuestionTx) DeleteUnusedByTexts(courseID int64, oldTexts, keepTexts []string) error
  func (t *QuestionTx) SetImportedFileHash(name, hash string) error
  ```

- [ ] **Step 1: Write failing tests** (`store.New(":memory:")`):
  - `TestInQuestionTxCommits`: insert 2 in `fn` returning nil → `QuestionCount() == 2`.
  - `TestInQuestionTxRollsBackOnError`: insert 1, return `errors.New("boom")` → returned error wraps `boom`, count `0`, and `GetImportedFileHash` set in the same `fn` is `""`.
  - `TestInQuestionTxRollsBackOnConstraint`: insert `{course 1, text "A", topic "x"}` then `{course 1, text "A", topic "y"}` → error, count `0`.
  - `TestQuestionTxFinders`: `FindByTopicAndTextNorm("x", NormalizeQuestionText("  a? "))` finds `"A"`; `FindByCourseAndText(1, "A")` finds it; misses return `nil, nil`.
  - `TestQuestionTxDeleteUnusedByTexts`: the same cases as the existing `DeleteUnusedQuestionsByTexts` tests (referenced by a thread → kept; in keep list → kept; otherwise deleted).

- [ ] **Step 2: Run, verify fail** — `go test ./internal/store/ -run 'QuestionTx' -v`.
- [ ] **Step 3: Implement** — `InQuestionTx`: `s.db.BeginTx(ctx, nil)`, `defer` rollback unless committed, wrap errors with `fmt.Errorf("question tx: %w", err)`. The DELETE query and the hash upsert are the same SQL as the existing `Store` methods, run on `t.tx`. `SetImportedFileHash` on `Store` stays for other callers.
- [ ] **Step 4: Run, verify pass** — `go test ./internal/store/ -count=1 && go build ./...`.
- [ ] **Step 5: Commit** — `feat(store): transactional question import primitives (#42)`.

---

### Task 6: `Apply`

**Files:**
- Create: `internal/questionimport/apply.go`
- Test: `internal/questionimport/apply_test.go` (with an in-memory `fakeTx`)

**Interfaces:**
- Consumes: `Draft` (Task 1), `Warning` (Task 3), `model.NormalizeQuestionText`.
- Produces:
  ```go
  type Tx interface { /* the six methods of *store.QuestionTx, same signatures (Task 5) */ }
  type Mode int
  const ( ModeSkipExisting Mode = iota; ModeUpdateExisting )
  type FileRecord struct{ Name, Hash string }
  type Options struct { Mode Mode; CourseID int64; OldTexts []string; File *FileRecord }
  type Result struct { Inserted, Updated, Skipped int; Warnings []Warning }
  func Apply(tx Tx, drafts []Draft, opts Options) (Result, error)
  ```
  Algorithm: spec §6 exactly. Warning params: `QImportWarnDupInFile {N, PrevN, Topic}`, `QImportWarnDupInDB {N, ID, Topic}` (topic of the draft), `QImportWarnDupOtherTopic {N, ID, Topic}` (topic of the existing row). In `ModeUpdateExisting` the DB match from step 3 updates (no warning); step 4 still skips with a warning in both modes. `keepTexts` = texts of all drafts. Any `Tx` error is returned wrapped; `Result` is then zero.

- [ ] **Step 1: Write failing tests**
  - `TestApplyInsertsAll`: 3 distinct drafts → `Inserted 3`, fake has 3 rows with `CourseID 1` and `TextNorm` set.
  - `TestApplyDupInFile`: drafts 1 and 3 equal after normalization, same topic → `Inserted 2, Skipped 1`, warning `{3, QImportWarnDupInFile, PrevN 1}`.
  - `TestApplyDupInFileDifferentTopic`: same text, different topics → for draft 2: step 4 hits draft 1's freshly inserted row → `QImportWarnDupOtherTopic`.
  - `TestApplyDupInDBSkip` / `TestApplyDupInDBUpdate`: existing row ID 7 → skip mode: warning `{1, QImportWarnDupInDB, ID 7}`; update mode: `Updated 1`, row 7 has the draft's rubric.
  - `TestApplyOldTexts`: `OldTexts ["A","B"]`, drafts `["A"]` → fake records `DeleteUnusedByTexts(1, ["A","B"], ["A"])`.
  - `TestApplyRecordsFile`: `File{Name:"f.json", Hash:"h"}` → fake records it; nil `File` → no call.
  - `TestApplyPropagatesError`: fake `Insert` returns error → `Apply` returns it.

- [ ] **Step 2: Run, verify fail** — `go test ./internal/questionimport/ -run TestApply -v`.
- [ ] **Step 3: Implement `Apply`.** Add a compile-time check in `internal/store/question_tx_test.go`: `var _ questionimport.Tx = (*QuestionTx)(nil)`.
- [ ] **Step 4: Run, verify pass** — `go test ./internal/questionimport/ ./internal/store/ -count=1`.
- [ ] **Step 5: Commit** — `feat(questionimport): transactional apply with duplicate planning (#42)`.

---

### Task 7: Startup load (`cmd/examiner`)

**Files:**
- Modify: `cmd/examiner/main.go` (`loadQuestions`, `runServe`, `runPrep`)
- Test: `cmd/examiner/main_test.go` (new, `package main`)

**Interfaces:**
- Consumes: `questionimport.Parse`, `Apply`, `Localize`, `Options{Mode: ModeSkipExisting, CourseID: 1, File: &FileRecord{path, hash}}`; `store.InQuestionTx`.
- Produces: `func loadQuestions(ctx context.Context, db *store.Store, paths []string, maxFollowups, timeLimit int) error`. `ctx` carries a localizer.

- [ ] **Step 1: Write failing tests** (`i18n.Init("en")`, `store.New(":memory:")`, files in `t.TempDir()`):
  - `TestLoadQuestionsInvalidFileFails`: 3 questions, errors in 1 (`rubric` empty) and 3 (`max_points` 0) → error message contains `Question 1, field rubric` and `Question 3, field max_points`; `QuestionCount() == 0`.
  - `TestLoadQuestionsInvalidEvenIfImported`: import a valid file, then `SetImportedFileHash(path, sha256(invalidContent))` and overwrite the file with invalid content → error (validation runs before the hash check).
  - `TestLoadQuestionsWrapperFormat`: `{"questions":[2 valid]}` → nil error, count 2, hash recorded.
  - `TestLoadQuestionsUnchangedSkips`: load the same valid file twice → count unchanged, nil error.

- [ ] **Step 2: Run, verify fail** — `go test ./cmd/examiner/ -v`.
- [ ] **Step 3: Implement** — per file: read → `Parse` → if errors, return `fmt.Errorf("%s:\n  %s", path, strings.Join(Localize(ctx, errs), "\n  "))` → existing hash logic (unchanged/changed skip) → `InQuestionTx(Apply)`; log warnings with `slog.Warn` using `LocalizeWarnings`. In `runServe` move the `appI18n.Init(lang)` block above `loadQuestions` and pass `appI18n.WithLocalizer(cmd.Context(), appI18n.NewLocalizer(lang))`. In `runPrep` call `appI18n.Init("en")` before `loadQuestions` and pass an `en` localizer context.
- [ ] **Step 4: Run, verify pass** — `go test ./cmd/examiner/ ./internal/questionimport/ -count=1`.
- [ ] **Step 5: Commit** — `feat: validate --questions files at startup and import atomically (#42)`.

---

### Task 8: Admin import

**Files:**
- Modify: `internal/handler/admin.go` (`handleUploadQuestions`)
- Modify: `internal/handler/views/admin_questions.templ` (`AdminQuestionsPage`)
- Modify: `internal/handler/admin.go` callers of `AdminQuestionsPage` (`handleAdminQuestionsPage`)
- Modify: `internal/handler/admin_test.go` (PR #73's test: expect localized English warnings)
- Test: `internal/handler/import_admin_test.go`

**Interfaces:**
- Consumes: `Parse`, `Apply`, `Localize`, `LocalizeWarnings`, `store.InQuestionTx`.
- Produces: `templ AdminQuestionsPage(flashMsg string, flashErr bool, errs []string, warnings []string)`. When `errs` is non-empty render `QImportErrorsTitle` and a `<ul>`; warnings as a second `<ul>`.

- [ ] **Step 1: Write failing tests** (helper `uploadAdmin(t, h, filename, content) *httptest.ResponseRecorder`, as in PR #73's test; `h := &Handler{store: s}`):
  - `TestAdminImportRejectsWholeFile`: 10 valid questions with question 7 `max_points: 2.5` → 400; body contains `Question 7, field max_points`; `QuestionCount() == 0`; `GetImportedFileHash(name) == ""`.
  - `TestAdminImportReportsAllErrors`: errors in questions 2 and 5 → body contains both.
  - `TestAdminImportRetryAfterFix`: invalid upload, then valid upload with the same filename → 200, count 10 (not "already imported").
  - `TestAdminImportSummaryAndWarnings`: one in-file duplicate → 200, body contains `Imported: 1, updated: 0, skipped: 1.` and `duplicates question 1`.
  - Update `TestAdminUploadDuplicateProtection` assertions to the English messages above.

- [ ] **Step 2: Run, verify fail** — `templ generate && go test ./internal/handler/ -run 'TestAdmin' -v`. Note: `handler.New` still compiles the schema until Task 9; these tests build `&Handler{store: s}` directly, so they run on Windows too.
- [ ] **Step 3: Implement** — order: multipart → read → hash "already imported" check (unchanged) → `Parse` → errors: 400 + page with `Localize` → `InQuestionTx(func(tx) { res, err = Apply(tx, drafts, Options{ModeSkipExisting, 1, nil, &FileRecord{header.Filename, hash}}) })` → error: `slog.Error` + 500 page with `QImportErrInternal` → 200 page with `Td("QImportSummary", …)` and warnings. Remove the now-unused `UploadSuccess` usage only if nothing else references it.
- [ ] **Step 4: Run, verify pass** — `go test ./internal/handler/ -run TestAdmin -count=1`.
- [ ] **Step 5: Commit** — `feat(admin): validated atomic question import with error list (#42)`.

---

### Task 9: Teacher save and constructor

**Files:**
- Modify: `internal/handler/teacher.go` (`handleTeacherUpload`, `handleTeacherCreateTest`; delete `parseQuestionsFromJSON`)
- Modify: `internal/handler/handler.go` (delete `compileQuestionSchema`, `questionSchema` field and jsonschema import; `New` keeps its signature and returns `nil` error)
- Modify: `internal/store/store.go`, `internal/store/store_test.go` (delete `UpdateQuestionByCourseAndText`, `DeleteUnusedQuestionsByTexts` and their tests; keep `stringsToAny`, `placeholders` if still used)
- Modify: `internal/handler/views/teacher_create_test.templ`
- Test: `internal/handler/import_teacher_test.go`

**Interfaces:**
- Consumes: `Parse`, `Decode`, `Apply`, `Localize`, `LocalizeWarnings`, `store.InQuestionTx`.
- Produces: `templ TeacherCreateTestPage(displayName, csrf string, existingQuestions []model.QuestionImport, filename, customName string, errs []string)`. Error list above the form (`QImportErrorsTitle` + `<ul>`); new-test `custom_filename` input gets `value={ customName }`. Form gets `data-msg-incomplete={ t(ctx,"QBuilderIncomplete") }` and `data-msg-empty={ t(ctx,"QBuilderEmpty") }`.

- [ ] **Step 1: Write failing tests** (each: `t.Chdir(t.TempDir())`, `os.Mkdir("questions", 0o755)`, teacher `teacher1` in context, direct `h.handleTeacherUpload` call, `h := &Handler{store: s}`):
  - `TestTeacherSaveRejectsWholeFile`: `questions_json` with 10 questions, error in 7 (`rubric: ""`) → 400; body contains `Question 7, field rubric`; count 0; `os.ReadDir("questions")` empty.
  - `TestTeacherSaveRerendersInput`: same request → body contains question 1's text inside `preloaded-data` and the submitted `custom_filename` value.
  - `TestTeacherSaveSuccess`: valid 2 questions, `custom_filename=mid` → 303 to `/teacher/profile`; `questions/` contains exactly `teacher_teacher1_mid.json` (no `.tmp-*`); count 2.
  - `TestTeacherEditInvalidKeepsOldState`: save valid `mid` (2 questions), then submit `editing_file=teacher_teacher1_mid.json`, `custom_filename=final`, invalid payload → 400; `questions/` still contains only `teacher_teacher1_mid.json` with original bytes; count 2 with original rubrics.
  - `TestTeacherEditRenameRemovesOld`: valid edit of `mid` renamed to `final` dropping one question → only `teacher_teacher1_final.json`; dropped question deleted from DB (not referenced by a thread).
  - `TestTeacherSaveTxFailureLeavesNoFile`: close the store before the request (`s.Close()`) so `InQuestionTx` fails → 500; body does not contain `sql:`; `questions/` has no files.

- [ ] **Step 2: Run, verify fail** — `templ generate && go test ./internal/handler/ -run TestTeacher -v`.
- [ ] **Step 3: Implement handler** — order per spec §7 "Teacher file write order": `Parse` → 400 re-render (drafts → `[]model.QuestionImport`, `editing_file`, `custom_filename`, `Localize`) → ownership check and name computation (unchanged) → old file `Decode` for `OldTexts` (ignore its errors) → `os.CreateTemp("questions", ".tmp-*.json")`, write, check `Close()` → `InQuestionTx(Apply{ModeUpdateExisting, 1, oldTexts, &FileRecord{safeName, hash}})`; on error remove temp, 500 → `os.Rename(tmp, outPath)` (error: `slog.Error`, 500) → remove old file if renamed → log warnings → redirect `h.path("/teacher/profile")`. Restore the comment `// TODO: derive course ID from context/config when multi-course support lands.` next to `CourseID: 1`. `handleTeacherCreateTest` uses `Decode` for prefill. Delete `compileQuestionSchema` and the field.
- [ ] **Step 4: Implement constructor changes** — add `required` to nothing (untouched blocks must stay submittable); in `assembleAndSubmit` a block is "started" if text or topic is non-empty; started blocks need text, topic, rubric, model answer non-empty and `Number.isInteger(+points) && +points >= 1`; alerts read `form.dataset.msgIncomplete` / `form.dataset.msgEmpty`. Points input gets `min="1" step="1"`.
- [ ] **Step 5: Run, verify pass** — `templ generate && go test ./... -count=1` → all packages PASS (the whole tree builds again).
- [ ] **Step 6: Commit** — `feat(teacher): validated atomic save, file published after commit (#42)`.

---

### Task 10: Cleanup, docs, full verification

**Files:**
- Modify: `internal/handler/handler.go` (`handleStartExam` dedup comment → `// Deduplicate by normalized text (guards against legacy DB duplicates).`)
- Modify: `README.md` (after the JSON format example: a "Question rules" table copied from spec §3, and one paragraph: an invalid file is rejected as a whole, every error is listed with question number and field, startup stops on an invalid `--questions` file)

- [ ] **Step 1: Edit comment and README.**
- [ ] **Step 2: Full verification** — `templ generate && go vet ./... && go test ./... -count=1 && golangci-lint run` → all clean.
- [ ] **Step 3: Grep for leftovers** — `grep -rn "questionSchema\|compileQuestionSchema\|parseQuestionsFromJSON\|UpdateQuestionByCourseAndText\|DeleteUnusedQuestionsByTexts" --include=*.go .` → no matches.
- [ ] **Step 4: Commit** — `docs: question rules and import errors (#42)`.
- [ ] **Step 5: Open PR** against `main`: title `feat: question validation and atomic import (#42)`; body states `Fixes #42`, depends on #73 (rebase after it merges), closes #74 items 2, 4 (import paths), 5, 6a, 6b; checklist item: validate `~/examiner/questions-{en,ru}.json` from `examiner-01` with a local start against a temp DB before deploying.
