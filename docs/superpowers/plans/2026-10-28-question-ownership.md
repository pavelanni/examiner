# Question Ownership (#44 stage A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Questions have an owner and a publication flag; a teacher cannot change or delete another owner's question through any path; students' exams use only published questions.

**Architecture:** Schema columns plus `model.Question.OwnerID/Published`; `internal/access.CanEditQuestion` as the single rule; `questionimport.Apply` takes an acting user, rejects another owner's questions with `*ConflictError`, and the store limits deletes by `owner_id`. Exam reads filter `published = 1`. A startup backfill assigns owners to existing rows.

**Tech Stack:** Go 1.25, modernc SQLite, templ, go-i18n.

**Spec:** `docs/superpowers/specs/2026-10-28-question-ownership-design.md`

## Global Constraints

- Branch `feature/issue-44-question-ownership`, based on PR #75's branch (`feature/issue-42-question-validation`).
- Run `templ generate` before building or testing `internal/handler`.
- Lint: `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...` → `0 issues.`
- Commit trailer: `Assisted-By: Claude Opus 5.5 <noreply@anthropic.com>`; never `Co-Authored-By`.
- `store` must not import `questionimport`; `questionimport` may import `store`-free packages only (`model`, `i18n`, `access`).
- `OwnerID == 0` means "no owner" in Go; `owner_id` is nullable in SQL and scanned with `sql.NullInt64`.
- Admin import and `--questions`/prep: `ModeSkipExisting`, `Published: true`. Teacher save: `ModeUpdateExisting`, `Published` from form field `publish == "1"`.
- New i18n IDs and exact copy are in spec §8; add both en and ru.

## Review Focus

- An admin saving through the teacher constructor updates a teacher's question in the same topic without a conflict (admin has full access), and the admin's file edit does not delete that teacher's question (test in Task 6).
- A teacher file with one own question and one foreign question is rejected as a whole: the own question's rubric is not updated either (test in Task 6).
- A `--questions` file containing a question that a teacher already owns starts the server normally: skipped with a warning, owner unchanged (test in Task 5).
- A question unpublished after a session started still shows on that session's review page, because review reads by `id` (test in Task 2).
- A teacher who unpublished a file, opens it again and saves without touching the checkbox keeps it unpublished (test in Task 6).

---

### Task 1: Schema, model fields, timestamps

**Files:**
- Modify: `internal/model/model.go` (`Question`)
- Modify: `internal/store/store.go` (`migrate`, every `SELECT ... FROM questions` that scans a `model.Question`, `InsertQuestion`, `UpdateQuestion`)
- Modify: `internal/store/question_tx.go` (`questionColumns`, `findOne`, `Insert`, `Update`)
- Test: `internal/store/ownership_test.go`

**Interfaces:**
- Produces: `model.Question` fields `OwnerID int64 \`json:"owner_id,omitempty"\`` and `Published bool \`json:"published"\``; `questions` columns `owner_id INTEGER REFERENCES users(id)`, `published INTEGER NOT NULL DEFAULT 1`, `created_at DATETIME`, `updated_at DATETIME`. `Insert`/`InsertQuestion` write `owner_id` (NULL when 0), `published`, `created_at = updated_at = now`; `Update`/`UpdateQuestion` write `published`, `updated_at = now` and leave `owner_id` unchanged.

- [ ] **Step 1: Write failing tests**
  - `TestMigrationAddsOwnershipColumns`: open a file DB with `database/sql` + `sqlitedb.DSN`, create the pre-#44 `questions` table (copy the current `CREATE TABLE questions` without the four columns, plus `text_norm`) and insert one row; close; `New(path)` → `GetQuestion(id)` has `Published == true`, `OwnerID == 0`; `SELECT created_at IS NOT NULL` is true; `New(path)` a second time succeeds and changes nothing.
  - `TestQuestionOwnershipRoundTrip`: `QuestionTx.Insert` with `OwnerID: u.ID, Published: false` (create the user with `CreateUser`) → `GetQuestion` returns both; `ListQuestions` returns both.
  - `TestQuestionTxUpdateKeepsOwner`: insert with owner A, `Update` the same ID with `OwnerID: 0, Published: false` → owner still A, `Published == false`, `updated_at >= created_at`.
- [ ] **Step 2: Run, verify fail** — `go test ./internal/store/ -run 'Ownership|Migration|UpdateKeepsOwner' -count=1` → compile error (`unknown field OwnerID`).
- [ ] **Step 3: Implement** — `ALTER TABLE questions ADD COLUMN ...` four times after the `text_norm` block, ignoring `isAlterDuplicate`; then `UPDATE questions SET created_at = ?, updated_at = ? WHERE created_at IS NULL`. Shared scan helper in `store.go`: `scanQuestion(row interface{ Scan(...any) error }) (model.Question, error)` reading `owner_id` via `sql.NullInt64`; use it in every question `SELECT`.
- [ ] **Step 4: Run, verify pass** — `go test ./internal/store/ ./internal/questionimport/ ./internal/handler/ ./cmd/examiner/ -count=1` → all `ok`.
- [ ] **Step 5: Commit** — `feat(store): question owner, publication flag and timestamps (#44)`.

---

### Task 2: Exams read only published questions

**Files:**
- Modify: `internal/store/store.go` (`ListDistinctTopics` → `ListPublishedTopics`, `ListQuestionsFiltered`)
- Modify: `internal/store/store_test.go` (`TestListDistinctTopics` → `TestListPublishedTopics`)
- Modify: `internal/handler/handler.go` (`handleIndex` caller)
- Test: `internal/store/ownership_test.go`, `internal/handler/ownership_test.go`

**Interfaces:**
- Produces: `func (s *Store) ListPublishedTopics() ([]string, error)` — topics with at least one `published = 1` row, sorted. `ListQuestionsFiltered(difficulty, topic string)` keeps its signature and adds `published = 1`.

- [ ] **Step 1: Write failing tests**
  - `TestListPublishedTopics`: topics `a` (published), `b` (only unpublished) → `["a"]`.
  - `TestListQuestionsFilteredSkipsUnpublished`: two questions in topic `a`, one unpublished → 1 result.
  - `TestStartExamUsesPublishedOnly` (handler): store with a published question P and an unpublished U in topic `t`; build the router like `newAnswerEnv` with a student and `h.handleStartExam` on `POST /exam/start` with `topic=t` → redirect; the created session's threads reference only P.
  - `TestReviewShowsUnpublishedQuestion` (handler): create a session on question Q, then set Q unpublished through `InQuestionTx`/`Update`; `GET /review/{id}` as a teacher → 200 and body contains Q's text.
- [ ] **Step 2: Run, verify fail** — `templ generate && go test ./internal/store/ ./internal/handler/ -run 'Published|StartExamUsesPublished|ReviewShowsUnpublished' -count=1`.
- [ ] **Step 3: Implement** — rename, add `WHERE published = 1` / `AND published = 1`, update the `handleIndex` call.
- [ ] **Step 4: Run, verify pass** — `go test ./... -count=1`.
- [ ] **Step 5: Commit** — `feat: exams and topic list use only published questions (#44)`.

---

### Task 3: `internal/access`

**Files:**
- Create: `internal/access/access.go`
- Test: `internal/access/access_test.go`

**Interfaces:**
- Produces: `func CanEditQuestion(u *model.User, q model.Question) bool`.

- [ ] **Step 1: Write failing test** — `TestCanEditQuestion`, table (user → question → want): admin(ID 1) / owned by 2 → true; admin / owner 0 → true; teacher(ID 2) / owner 2 → true; teacher(ID 2) / owner 3 → false; teacher / owner 0 → false; student(ID 2) / owner 2 → false; nil / owner 2 → false.
- [ ] **Step 2: Run, verify fail** — `go test ./internal/access/` → `undefined: CanEditQuestion`.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run, verify pass** — `go test ./internal/access/ -count=1`.
- [ ] **Step 5: Commit** — `feat(access): CanEditQuestion permission rule (#44)`.

---

### Task 4: Store helpers for owners and publication

**Files:**
- Create: `internal/store/ownership.go`
- Test: `internal/store/ownership_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Store) FirstAdminID() (int64, error)                                  // MIN(id) with role 'admin'; error "no admin user" if none
  func (s *Store) FindUnownedQuestionIDs(topic, textNorm string) ([]int64, error) // owner_id IS NULL
  func (s *Store) ClaimQuestionOwners(claims map[int64][]int64) error            // spec §2 steps 3–4, one tx
  type QuestionKey struct{ Topic, TextNorm string }
  func (s *Store) AllPublished(ownerID int64, keys []QuestionKey) (bool, error)  // no matching rows → true
  ```

- [ ] **Step 1: Write failing tests**
  - `TestFirstAdminID`: users teacher(1), admin(2), admin(3) → 2; no admin → error.
  - `TestFindUnownedQuestionIDs`: two rows same key, one owned → returns only the unowned ID.
  - `TestClaimQuestionOwners`: rows R1, R2, R3 unowned, R4 owned by T2; claims `{R1:[T1], R2:[T1,T2], R4:[T1]}` → R1→T1, R2→admin, R3→admin, R4 stays T2; calling again with the same claims changes nothing; with no admin user → error and no row changed.
  - `TestClaimQuestionOwnersSameUserTwice`: `{R1:[T1,T1]}` → T1 (duplicates of one user are one claim).
  - `TestAllPublished`: owner T1 rows (a,x) published, (b,y) unpublished; keys `[(a,x)]` → true; `[(a,x),(b,y)]` → false; `[(zz,zz)]` → true; a row of another owner that is unpublished does not affect T1's answer.
- [ ] **Step 2: Run, verify fail** — `go test ./internal/store/ -run 'FirstAdmin|Unowned|Claim|AllPublished' -count=1`.
- [ ] **Step 3: Implement** — `ClaimQuestionOwners` applies updates only `WHERE owner_id IS NULL`, logs `slog.Warn("question claimed by several teachers", "id", id, "users", ids)` for multi-user claims, and assigns `FirstAdminID` to every remaining NULL row, all in one `BeginTx`.
- [ ] **Step 4: Run, verify pass** — `go test ./internal/store/ -count=1`.
- [ ] **Step 5: Commit** — `feat(store): owner backfill and publication lookups (#44)`.

---

### Task 5: `Apply` with an acting user

**Files:**
- Modify: `internal/questionimport/apply.go`, `internal/questionimport/localize.go`
- Modify: `internal/store/question_tx.go` (`DeleteUnusedByTexts` owner, new `UserDisplayName`)
- Modify: `internal/i18n/locales/active.{en,ru}.json` (spec §8)
- Modify callers to pass the actor: `internal/handler/admin.go`, `internal/handler/teacher.go`, `cmd/examiner/main.go` (`loadQuestions` gets the actor from `FirstAdminID` + `GetUserByID`)
- Test: `internal/questionimport/apply_test.go`, `internal/questionimport/localize_test.go`, `cmd/examiner/main_test.go`, `internal/store/question_tx_test.go`

**Interfaces:**
- Consumes: `access.CanEditQuestion` (Task 3), `store.FirstAdminID` (Task 4).
- Produces:
  ```go
  // Options additions
  Actor     *model.User
  Published bool
  // Tx changes
  DeleteUnusedByTexts(courseID, ownerID int64, oldTexts, keepTexts []string) error
  UserDisplayName(id int64) (string, error)
  // new error type
  type ConflictError struct{ Errs []FieldError }
  func (e *ConflictError) Error() string // "N question(s) belong to another owner"
  ```
  Conflict entry: `FieldError{Question: N, Code: "QImportErrNotOwner", Params: {"ID": existing.ID, "Owner": name, "Topic": d.Topic}}`; `Owner` is `""` when `existing.OwnerID == 0`, and `Localize` replaces an empty `Owner` for this code with `T("QImportOwnerAdmin")`.

- [ ] **Step 1: Write failing tests**
  - Apply (fake Tx; extend `fakeTx` with `users map[int64]string`, record `deletes` with owner, implement `UserDisplayName`):
    - `TestApplyNilActor` → error, no writes.
    - `TestApplyInsertSetsOwnerAndPublished`: actor T1, `Published:false` → row `OwnerID == 1`, `Published == false`.
    - `TestApplyUpdatesOwnQuestion`: row owned by T1, actor T1, `ModeUpdateExisting`, `Published:false` → updated, `OwnerID` still 1, `Published == false`.
    - `TestApplyConflictWithOtherOwner`: row 17 owned by T2 ("Ivanov"), actor T1 → `errors.As(err, &*ConflictError)`; `Errs == [{1, "", "QImportErrNotOwner"}]`, `Params["ID"] == int64(17)`, `Params["Owner"] == "Ivanov"`; row 17 unchanged; no delete or hash recorded.
    - `TestApplyTwoConflicts`: two foreign questions → both listed in order.
    - `TestApplyAdminActorUpdatesAnyQuestion`: actor admin, `ModeUpdateExisting`, row owned by T2 → updated, no conflict, owner still T2.
    - `TestApplySkipModeIgnoresOwner`: `ModeSkipExisting`, row owned by T2, actor admin → skipped with `QImportWarnDupInDB`, no conflict.
    - `TestApplyDeleteScopedToActor`: `OldTexts` set, actor T1 → fake records `ownerID == 1`.
  - `TestLocalizeNotOwnerAdmin` (ru): `{3, "", "QImportErrNotOwner", {ID:17, Owner:"", Topic:"Механика"}}` → `Вопрос №3: совпадает с вопросом #17 владельца «администратор» в теме «Механика»; уберите его из файла`. Add the three new IDs to `TestLocalesComplete`.
  - Store: `TestQuestionTxDeleteUnusedScopedToOwner`: rows "A" owned by T1 and "B" owned by T2; `DeleteUnusedByTexts(1, T1, ["A","B"], nil)` → A deleted, B kept. `TestQuestionTxUserDisplayName`: returns `display_name`; unknown ID → `""`, nil error.
  - `TestLoadQuestionsSkipsTeacherOwned` (cmd): a row owned by a teacher, `--questions` file with the same question → nil error, owner unchanged; a new question in the same file → owner is the admin.
- [ ] **Step 2: Run, verify fail** — `go test ./internal/questionimport/ ./internal/store/ ./cmd/examiner/ -count=1`.
- [ ] **Step 3: Implement** — `Apply` collects conflicts and returns `&ConflictError{}` after the loop (before delete and hash); otherwise as spec §4. Admin handler passes `Actor: user, Published: true`; teacher handler passes `Actor: user, Published: true` for now (the checkbox arrives in Task 6); `loadQuestions` resolves the admin once before the file loop.
- [ ] **Step 4: Run, verify pass** — `templ generate && go test ./... -count=1`.
- [ ] **Step 5: Commit** — `feat(questionimport): owner-aware import with conflicts (#44)`.

---

### Task 6: Teacher save: conflicts and the "Publish" checkbox

**Files:**
- Modify: `internal/handler/teacher.go`, `internal/handler/admin.go`
- Modify: `internal/handler/views/teacher_create_test.templ` (+ `teacher_create_test_test.go` call)
- Test: `internal/handler/ownership_test.go`

**Interfaces:**
- Consumes: `*questionimport.ConflictError` (Task 5), `store.AllPublished`, `store.QuestionKey` (Task 4).
- Produces: `templ TeacherCreateTestPage(displayName, csrf string, existingQuestions []model.QuestionImport, filename, customName string, published bool, errs []string)`; `renderTeacherCreateTest(..., published bool, errs []string)`.

- [ ] **Step 1: Write failing tests** (reuse `teacherEnv`, `teacherSave`; add `teacherSaveAs(t, h, user, fields)` that takes the acting user; create users with `CreateUser`):
  - `TestTeacherCannotOverwriteOthersQuestion`: A saves `mid` (1 question, rubric "r"); B saves a file with the same question and rubric "hacked" → 400; body contains `matches question #` and A's display name; A's row rubric still "r"; `questions/` has only A's file.
  - `TestTeacherConflictRollsBackOwnQuestions`: B owns question X; B saves a file with X (rubric changed) and A's question → 400; X's rubric unchanged.
  - `TestTeacherEditDoesNotDeleteOthersQuestion`: insert "Shared" unowned and assign it to A with `ClaimQuestionOwners`; write B's old file `questions/teacher_B_mid.json` directly with "Shared" and one B question; B saves an edit of that file without "Shared" → 303; A's "Shared" row still exists.
  - `TestAdminConstructorUpdatesTeacherQuestion`: admin saves via constructor a file containing A's question with a new rubric → 303; row updated, owner still A; admin then edits its file dropping that question → A's row still exists.
  - `TestTeacherSaveUnpublished`: `publish` absent → question not in `ListPublishedTopics`.
  - `TestTeacherSavePublishedByDefaultField`: `publish=1` → topic listed.
  - `TestConstructorCheckboxReflectsDB`: save `mid` unpublished; `GET /teacher/create-test?file=teacher_teacher1_mid.json` (call `h.handleTeacherCreateTest`) → body has the `publish` input without `checked`; for a new file (no `file`) the input is `checked`.
  - `TestTeacherResaveKeepsUnpublished`: save unpublished, open (checkbox unchecked), save again without `publish` → still unpublished.
  - `TestAdminImportConflictImpossible`: admin import of a file containing a teacher-owned question → 200 with `QImportWarnDupInDB` text, no 400.
- [ ] **Step 2: Run, verify fail** — `templ generate && go test ./internal/handler/ -run 'Teacher|Admin|Constructor' -count=1`.
- [ ] **Step 3: Implement** — after `InQuestionTx`, `var ce *questionimport.ConflictError; if errors.As(err, &ce)` → remove temp file, re-render 400 with `Localize(ctx, ce.Errs)` and the submitted `published`. `handleTeacherCreateTest`: for an edited file, decode it, build `[]store.QuestionKey` with `model.NormalizeQuestionText`, `AllPublished(user.ID, keys)`; new file → `true`. Checkbox markup and label per spec §7–8. Admin handler: treat `ConflictError` the same way (defensive; not reachable with `ModeSkipExisting`).
- [ ] **Step 4: Run, verify pass** — `templ generate && go test ./... -count=1`.
- [ ] **Step 5: Commit** — `feat(teacher): reject saves that touch others' questions; publish checkbox (#44)`.

---

### Task 7: Owner backfill at startup

**Files:**
- Create: `cmd/examiner/backfill.go`
- Modify: `cmd/examiner/main.go` (`runServe`, `runPrep`)
- Test: `cmd/examiner/backfill_test.go`

**Interfaces:**
- Consumes: `store.ListUsers`, `FindUnownedQuestionIDs`, `ClaimQuestionOwners` (Task 4); `questionimport.Decode`; `model.NormalizeQuestionText`.
- Produces: `func backfillQuestionOwners(db *store.Store, dir string) error`. A missing `dir` is not an error.

- [ ] **Step 1: Write failing tests** (temp dir with files; store with users created via `CreateUser`; unowned rows inserted with `QuestionTx.Insert` and `OwnerID: 0`):
  - `TestBackfillAssignsFromFiles`: `teacher_ivan_mid.json` with Q1, `teacher_olga_final.json` with Q2 → Q1→ivan, Q2→olga, Q3 (in no file)→admin.
  - `TestBackfillSharedQuestionGoesToAdmin`: Q1 in both teachers' files → admin.
  - `TestBackfillUnknownUserIgnored`: `teacher_ghost_x.json` → its questions go to admin.
  - `TestBackfillLongestUsernamePrefix`: users `ivan` and `ivan_mid`, file `teacher_ivan_mid_term.json` → owner `ivan_mid`; with only `ivan` → `ivan`.
  - `TestBackfillIdempotent`: run twice → same owners; already-owned rows never change.
  - `TestBackfillMissingDir`: dir does not exist → nil error, unowned rows go to admin.
- [ ] **Step 2: Run, verify fail** — `go test ./cmd/examiner/ -run Backfill -count=1`.
- [ ] **Step 3: Implement**; call it in `runServe` right after `seedAdmin` and in `runPrep` right after the admin user is created, with `"questions"` and with the manifest's questions directory respectively; wrap errors as `fmt.Errorf("backfill question owners: %w", err)`.
- [ ] **Step 4: Run, verify pass** — `go test ./cmd/examiner/ -count=1`.
- [ ] **Step 5: Commit** — `feat: assign owners to existing questions at startup (#44)`.

---

### Task 8: Docs, full verification, PR

**Files:**
- Modify: `README.md` (teacher section: ownership, the "Publish" checkbox, conflicts; one line that exams use published questions only)

- [ ] **Step 1: Edit README.**
- [ ] **Step 2: Full verification** — `templ generate && go vet ./... && go test ./... -count=1` → all `ok`; lint → `0 issues.`
- [ ] **Step 3: Commit** — `docs: question ownership and publication (#44)`.
- [ ] **Step 4: Final whole-branch review, fix pass, then PR** against `main`: title `feat: question ownership and edit permissions (#44, stage A)`; body: `Part of #44 (stage A)`, depends on #75 and #73, the stage table from the spec, rollout note (check the log for "question claimed by several teachers" after the first start).
