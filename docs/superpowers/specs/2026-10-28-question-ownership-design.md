# Question Ownership and Edit Permissions Design (#44, stage A)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:writing-plans to create the implementation plan after this spec is approved.

**Goal:** A teacher cannot change or delete another teacher's question, neither through the UI nor by a direct request. The admin keeps full access. Only published questions reach students' exams.

**Architecture:** Questions get an owner and a publication flag in the DB. One permission function, `access.CanEditQuestion`, decides edits; `questionimport.Apply` calls it for every question it matches and rejects the whole save on a conflict. Deletes are also limited by `owner_id` in SQL. Exam reads filter on `published = 1`.

**Tech Stack:** Go 1.25, modernc SQLite, templ, go-i18n. Builds on PR #75 (#42), which builds on PR #73 (#43).

---

## Background

ТЗ п. 3.2, stage 3, readiness criterion 2:

- a teacher changes only their own questions and topics;
- other teachers' published questions are read-only for them;
- the administrator has full access to the shared question bank.

Today `questions` has no owner. "Ownership" exists only as the `teacher_<username>_` prefix of files in `questions/`. A teacher save updates any question in the same topic with the same normalized text (PR #73, kept in PR #75), so teacher B can overwrite teacher A's rubric, model answer and points. Deleting questions dropped from an edited file matches by text only, so it can delete another teacher's question.

### Scope: stage A of #44

Issue #44 is split into stages, each with its own spec, plan and PR:

| Stage | Content |
| --- | --- |
| **A (this spec)** | Owner and publication in the DB, the permission function, owner-safe imports, published-only exam reads, the "Publish" checkbox |
| B | Teacher "My questions" and "Shared bank" pages, per-question CRUD by `id`, `CanViewQuestion` |
| C | Admin question list with filters, editing, changing the owner |
| D | Topic rename and delete rules |

Stage A alone meets the #44 acceptance criterion: a teacher cannot change another teacher's question through the UI or by a direct request.

### Decisions taken during brainstorming

| Question | Decision |
| --- | --- |
| Where to build | `cmd/examiner` on SQLite, like #42 and #43, not the management track of `runtime-design.md` yet |
| What "published" means | In students' exams **and** visible to other teachers. Existing, admin-imported and `--questions` questions are published. Teachers choose with a checkbox, on by default. |
| Owners of existing questions | From `questions/teacher_<user>_*.json`, otherwise the first admin |
| Teacher file contains another owner's question | The whole save is rejected with a 400 that lists the conflicting questions |
| Where the rule lives | A `internal/access` package plus owner-scoped SQL for deletes |

## Design

### 1. Data model and migration

New columns on `questions`, added with `ALTER TABLE ... ADD COLUMN` (no-op when they exist, like the existing migrations):

| Column | Definition | Existing rows |
| --- | --- | --- |
| `owner_id` | `INTEGER REFERENCES users(id)` (nullable) | `NULL` until the owner backfill (§2) |
| `published` | `INTEGER NOT NULL DEFAULT 1` | `1`, so everything in exams today stays there |
| `created_at` | `DATETIME` (nullable) | set to the migration time where `NULL` |
| `updated_at` | `DATETIME` (nullable) | set to the migration time where `NULL` |

SQLite does not allow a non-constant default in `ADD COLUMN`, so the timestamps are filled by an `UPDATE ... WHERE created_at IS NULL` in the same migration step.

`model.Question` gets two fields:

```go
OwnerID   int64 `json:"owner_id,omitempty"` // 0 = no owner
Published bool  `json:"published"`
```

Every `SELECT` that scans a `model.Question` (`ListQuestions`, `ListQuestionsFiltered`, `GetQuestion`, `FindQuestionByTopicAndTextNorm`, `QuestionTx.findOne`) reads both, with `owner_id` through `sql.NullInt64`. Timestamps get struct fields in stages B and C, which sort by them; in stage A only SQL writes them.

Inserts set `created_at` and `updated_at` to now; `QuestionTx.Update` sets `updated_at` to now.

### 2. Owner backfill

`backfillQuestionOwners(db, dir)` in `cmd/examiner` runs in `runServe` and `runPrep` after `seedAdmin` (or the prep admin user) and before `loadQuestions`, so an admin exists.

1. List `dir/teacher_*.json`. A file belongs to the user whose username gives the **longest** prefix `teacher_<username>_` of the file name (usernames and file names may both contain `_`); skip the file if no user matches. Decode it with `questionimport.Decode` (errors ignored; the backfill takes what it can read).
2. For every decoded question, find rows with `owner_id IS NULL` and the same `(topic, text_norm)`. Record the claim `row id → user id`.
3. A row claimed by exactly one user gets that owner. A row claimed by two or more different users is left for step 4 and logged with `slog.Warn("question claimed by several teachers", "id", …, "users", …)`.
4. Every row still with `owner_id IS NULL` gets the first admin (`MIN(id)` among users with role `admin`). If there is no admin, the step returns an error.

All updates run in one transaction. The step only touches `owner_id IS NULL` rows, so a second run changes nothing.

The file reading and decoding live in `cmd/examiner`, because `store` must not import `questionimport` (the dependency runs the other way). It calls these store methods:

```go
func (s *Store) ListUsers() ([]model.User, error)                              // exists
func (s *Store) FindUnownedQuestionIDs(topic, textNorm string) ([]int64, error) // owner_id IS NULL rows
func (s *Store) ClaimQuestionOwners(claims map[int64][]int64) error            // row id -> claiming user ids; steps 3 and 4 in one tx
```

`store.FirstAdminID() (int64, error)` is also used by `loadQuestions` and `runPrep` (§4).

### 3. Permission rule

New package `internal/access`, depending only on `internal/model`:

```go
// CanEditQuestion reports whether u may change or delete q: an admin may
// change any question, a teacher only their own. Everyone else, and a
// nil user, may not.
func CanEditQuestion(u *model.User, q model.Question) bool
```

A question with `OwnerID == 0` is editable by admins only. `CanViewQuestion` is added in stage B together with its first caller.

### 4. Import with an acting user

`questionimport.Options` gets:

```go
Actor     *model.User // who imports; becomes the owner of inserted questions
Published bool        // publication state of inserted and updated questions
```

`Tx` gets `UserDisplayName(id int64) (string, error)`, and `DeleteUnusedByTexts` gets an owner:

```go
DeleteUnusedByTexts(courseID, ownerID int64, oldTexts, keepTexts []string) error
```

`Apply`, per question in file order (changes from #42 in bold):

1. In-file duplicate → skip with `QImportWarnDupInFile` (unchanged).
2. Same `(topic, text_norm)` exists (`existing`):
   - `ModeSkipExisting` → skip with `QImportWarnDupInDB` (unchanged; admin and startup).
   - `ModeUpdateExisting`: **if `!access.CanEditQuestion(opts.Actor, *existing)`, record a conflict** `FieldError{Question: N, Code: "QImportErrNotOwner", Params: {ID, Owner, Topic}}` and continue with the next question. Otherwise the #42 collision check, then `Update` with `Published = opts.Published`, keeping `existing.OwnerID`.
3. Same exact text in another topic → skip with `QImportWarnDupOtherTopic` (unchanged; this never modifies the other row).
4. Insert with **`OwnerID = opts.Actor.ID`, `Published = opts.Published`**.

After the loop, if any conflict was recorded, `Apply` returns `&ConflictError{Errs: conflicts}` and writes nothing more; the caller's transaction rolls back. Otherwise `DeleteUnusedByTexts(courseID, opts.Actor.ID, OldTexts, keepTexts)` (only when `OldTexts` is non-empty) and the file hash, as in #42.

```go
// ConflictError lists questions that belong to someone else. Callers show
// it like validation errors.
type ConflictError struct{ Errs []FieldError }
func (e *ConflictError) Error() string
```

`Owner` in the params is the owner's display name from `Tx.UserDisplayName`; for `OwnerID == 0` it is the localized "administrator" (`QImportOwnerAdmin`).

`QuestionTx.DeleteUnusedByTexts` adds `AND owner_id = ?` to its `WHERE`. This is the second line of defense: even if `OldTexts` contains another teacher's text, their row is not deleted.

`Apply` returns an error if `opts.Actor` is nil.

### 5. Entry points

| Entry point | `Actor` | `Mode` | `Published` |
| --- | --- | --- | --- |
| Admin `POST /admin/questions` | the logged-in admin | `ModeSkipExisting` | `true` |
| `--questions` (`runServe`) and `runPrep` | the first admin (`FirstAdminID`, loaded with `GetUser`) | `ModeSkipExisting` | `true` |
| Teacher `POST /teacher/tests` | the logged-in teacher (or admin using the constructor) | `ModeUpdateExisting` | the `publish` form field (`"1"` = true) |

Handlers treat `*questionimport.ConflictError` from `InQuestionTx` like validation errors: 400, the page shown again with `questionimport.Localize(ctx, conflict.Errs)`, and the temp file removed. Any other error stays a 500 as in #42.

`loadQuestions` returns the localized conflict list as an error like validation errors, but with `ModeSkipExisting` it cannot produce conflicts; the branch exists for completeness only.

### 6. Exam reads

- `ListDistinctTopics` is renamed `ListPublishedTopics` and returns topics that have at least one question with `published = 1`. Its only caller is the topic list in `handleIndex`.
- `ListQuestionsFiltered` adds `published = 1`. Its callers are the exam preview in `handleIndex` and `handleStartExam`.
- Review pages and grading read questions by `id` through `question_threads`, so started sessions keep showing questions that were later unpublished.

### 7. Constructor

`TeacherCreateTestPage` gets `published bool`. The form gets a checkbox:

```html
<label><input type="checkbox" name="publish" value="1" checked={ published }/> { t(ctx, "QPublishLabel") }</label>
```

- New file: checked.
- Editing a file: checked only if every question of the file that exists in the DB and belongs to the teacher is published. Lookup:
  ```go
  type QuestionKey struct{ Topic, TextNorm string } // in package store
  func (s *Store) AllPublished(ownerID int64, keys []QuestionKey) (bool, error)
  ```
  No matching rows → `true`.
- After a 400 or 500: the submitted value.

`handleTeacherUpload` reads `publish` with `r.FormValue("publish") == "1"`.

### 8. i18n

New message IDs in `active.en.json` and `active.ru.json`:

| ID | en | ru |
| --- | --- | --- |
| `QImportErrNotOwner` | `matches question #{{.ID}} owned by {{.Owner}} in topic "{{.Topic}}"; remove it from the file` | `совпадает с вопросом #{{.ID}} владельца «{{.Owner}}» в теме «{{.Topic}}»; уберите его из файла` |
| `QImportOwnerAdmin` | `the administrator` | `администратор` |
| `QPublishLabel` | `Publish (questions go into exams and are visible to other teachers)` | `Опубликовать (вопросы попадают в экзамены и видны другим преподавателям)` |

`QImportErrNotOwner` renders through `QImportErrAtQuestion`: «Вопрос №3: совпадает с вопросом #17 владельца «Иванов» в теме «Механика»; уберите его из файла».

### 9. Testing

- `access`: table over admin / teacher-owner / teacher-other / student / nil × question with owner / `OwnerID == 0`.
- `store`:
  - migration on a DB that already has questions: `published = 1`, `created_at` set, `owner_id` NULL; running `migrate` again changes nothing;
  - `DeleteUnusedByTexts` with an owner keeps another owner's row with the same text;
  - `ListPublishedTopics` and `ListQuestionsFiltered` skip unpublished rows;
  - `AllPublished`: all published → true, one unpublished → false, no rows → true;
  - `ClaimQuestionOwners`: single claim, double claim → admin, unclaimed → admin, no admin → error, second run no-op.
- `cmd/examiner`: `backfillQuestionOwners` with two teacher files in a temp dir, one unknown username, one shared question.
- `questionimport.Apply` (fake Tx): own question updated with `Published` from options; another owner's question → `*ConflictError` naming N, ID and owner, the other owner's row unchanged, and no delete or hash call (inserts made before the loop ended are undone by the caller's transaction rollback); two conflicts in one file → both listed; admin mode skips; inserts carry `OwnerID` and `Published`; nil actor → error.
- `cmd/examiner`: `backfillQuestionOwners` maps `teacher_ivan_mid_term.json` to user `ivan` when users `ivan` exists and `ivan_mid` does not, and to `ivan_mid` when both exist.
- `handler` (acceptance):
  - teacher A saves a file; teacher B saves a file with the same question → 400 naming A's question; A's rubric and points unchanged; no file written for B;
  - B edits B's own file whose old version contained A's question text (inserted by the admin under A in the test setup) → A's question still exists;
  - admin import → owner is the admin;
  - teacher saves with the checkbox off → question not in `ListPublishedTopics` and not in the exam started for that topic;
  - constructor edit of a file whose questions are unpublished renders the checkbox unchecked.

## Out of scope

- Stages B, C, D of #44 (see the table above).
- Question versioning or snapshots in `question_threads` (issue's second open question). To be filed as a separate issue.
- Per-course access for exams (comment on #44).
- The `(course_id, text)` vs `(topic, text_norm)` identity model (#74 item 1).

## Rollout

- Branch `feature/issue-44-question-ownership` from PR #75's branch; the PR depends on #75 and #73.
- On first start after deploy, the backfill assigns owners from the host's `questions/teacher_*.json`. Check the log for "question claimed by several teachers".
