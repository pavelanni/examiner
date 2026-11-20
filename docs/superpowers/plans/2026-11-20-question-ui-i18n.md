# Question Constructor UI and Full Russian Localization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Progress is tracked in issue #105, not in this
> file.

**Goal:** A teacher who has never seen Examiner can create and upload a question set from the labels alone, and the
Russian UI shows no English (#105).

**Architecture:** Label helpers and a localized `httpError` remove English from all pages; the constructor renders its
question block once in a templ `<template>` and JS only clones it; the profile lists sets by name with a success
message; a format help component and an example file document the JSON format.

**Tech Stack:** Go 1.25, templ, go-i18n, htmx 2, Pico CSS v2.

**Spec:** `docs/superpowers/specs/2026-11-20-question-ui-i18n-design.md`

## Global Constraints

- Every new message ID goes into both `internal/i18n/locales/active.en.json` and `active.ru.json`.
- English values of existing IDs and of the generic error IDs stay byte-identical to today's texts: handler tests
  assert them with the English locale.
- Form field names posted by the constructor do not change: `custom_filename`, `editing_file`, `questions_json`,
  `question_ids`, `questions_file`, `publish`, `dup_token`, `dup_<N>`, `q_text_<i>`, `q_topic_<i>`, `q_diff_<i>`,
  `q_points_<i>`, `q_rubric_<i>`, `q_answer_<i>`.
- All links and actions go through `p(ctx, …)` in views and `h.path(…)` in handlers (`--base-path /ru`).
- No new dependencies. `templ generate` before `go build`/`go test`.
- Each task ends green: `templ generate && go vet ./... && go test ./...`; the last task also runs
  `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run --timeout=5m` (0 issues).
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- Editing a set and saving it without touching the name must keep the file name (today it gains a second
  `teacher_<username>_` prefix on every save). Test in Task 4.
- A set name the teacher types with their own prefix (`teacher_teacher1_mid`) must not be doubled either. Test in
  Task 4.
- File tab selected, then switched back to manual: the stale file must not be posted (the server prefers
  `questions_file`). Covered by disabling the input; asserted in the render test of Task 6.
- Russian page with the duplicate dialog of #79 open: still fully Russian, and the hidden kept decisions survive the
  new markup. Existing `duplicates_test.go` plus the render test of Task 7.
- Profile success message with a hostile `saved` value (`<script>`) or non-numeric counts: escaped, or no message.
  Test in Task 4.

---

### Task 1: Label helpers for status, role and difficulty; fixed strings

**Files:**
- Create: `internal/handler/views/labels.go`, `internal/handler/views/labels_test.go`
- Modify: `internal/handler/views/session_reset.templ` (remove `sessionStatusLabel`), `layout.templ:125`,
  `admin_users.templ:48-50,81`, `results.templ:58`, `review.templ:72`, `thread.templ:17`, `index.templ:209`,
  `evaluator_message.templ:8`, `exam.templ:41`, both locale files

**Interfaces:**
- Produces: `sessionStatusLabel(ctx context.Context, s model.SessionStatus) string`,
  `roleLabel(ctx context.Context, r model.UserRole) string`,
  `difficultyLabel(ctx context.Context, d model.Difficulty) string` in package `views`.

New IDs (en / ru):

| ID | en | ru |
| --- | --- | --- |
| `StatusInProgress` | In progress | Идёт экзамен |
| `RoleStudent` | Student | Студент |
| `RoleTeacher` | Teacher | Преподаватель |
| `RoleAdmin` | Administrator | Администратор |
| `DifficultyEasy` | Easy | Лёгкий |
| `DifficultyMedium` | Medium | Средний |
| `DifficultyHard` | Hard | Сложный |
| `FollowupQuestion` | Follow-up question: | Уточняющий вопрос: |
| `TimeUp` | Time's up! Please submit your exam. | Время вышло. Сдайте экзамен. |

Status mapping (reuse existing IDs): `in_progress`→`StatusInProgress`, `submitted`→`StatementSubmitted`,
`grading`→`StatementGrading`, `grading_failed`→`SessionGradingFailed`, `graded`→`StatementGraded`,
`reviewed`→`StatementReviewed`, `annulled`→`AttemptAnnulled`; any other value → the raw value.

- **Step 1: Write the failing tests** in `labels_test.go`: `TestSessionStatusLabelAll` renders every status in
  `ru` and asserts none equals its raw value and `graded`→`Ожидает проверки`, `in_progress`→`Идёт экзамен`;
  `TestRoleLabel` (`ru`: `Студент`, `Преподаватель`, `Администратор`; `en`: `Student`, `Teacher`, `Administrator`);
  `TestDifficultyLabel` (`ru`: `Лёгкий`, `Средний`, `Сложный`). Use `i18n.Init` and
  `i18n.WithLocalizer(ctx, i18n.NewLocalizer(lang))` as in `teacher_create_test_test.go`.
- **Step 2: Run** `templ generate && go test ./internal/handler/views/ -run 'Label'` → FAIL (undefined helpers).
- **Step 3: Implement** the three helpers in `labels.go`, move `sessionStatusLabel` out of
  `session_reset.templ`, add the IDs, and replace the raw outputs listed under Files: layout role
  `({ roleLabel(ctx, user.Role) })`; admin users role column and the three `<option>` labels (values unchanged);
  difficulty in results, review and thread; index difficulty filter: split the comma-separated
  `d.Config.Difficulty`, map each through `difficultyLabel`, join with `", "`; `FollowupQuestion`; `TimeUp`.
- **Step 4: Run** `templ generate && go test ./...` → PASS.
- **Step 5: Commit** `fix(i18n): translate statuses, roles, difficulty and fixed texts (#105)`.

### Task 2: Localized handler errors

**Files:**
- Create: `internal/handler/httperror.go`, `internal/handler/httperror_test.go`
- Modify: every non-test `internal/handler/*.go` that calls `http.Error` with a string literal; both locale files

**Interfaces:**
- Produces: `func httpError(w http.ResponseWriter, r *http.Request, status int, msgID string)` in package `handler`:
  `http.Error(w, appI18n.T(r.Context(), msgID), status)`.

Generic IDs keep today's English exactly: `ErrInternal` "internal error", `ErrForbidden` "forbidden",
`ErrUnauthorized` "unauthorized", `ErrNotFound` "not found", `ErrBadRequest` "bad request". Russian:
"Внутренняя ошибка. Попробуйте ещё раз; если ошибка повторится, сообщите администратору.", "Недостаточно прав.",
"Войдите в систему.", "Не найдено.", "Некорректный запрос.". Every other literal gets its own ID named `Err` +
CamelCase of the text, English value = the literal (e.g. `ErrUsernameExists` "username already exists" /
"Пользователь с таким логином уже существует."). `parseIDParam`'s `"invalid "+name` → `ErrBadRequest`. `/healthz`
is not changed.

- **Step 1: Write the failing test** `TestHTTPErrorLocalized`: with a `ru` localizer in the request context,
  `httpError(rec, req, 403, "ErrForbidden")` → status 403, body `Недостаточно прав.\n`; with `en` → `forbidden\n`.
- **Step 2: Run** `go test ./internal/handler/ -run TestHTTPErrorLocalized` → FAIL (undefined).
- **Step 3: Implement** `httpError`, add the IDs, replace the literals. Check with
  `grep -n 'http.Error(w, "' internal/handler/*.go | grep -v _test` → only `/healthz` lines remain.
- **Step 4: Run** `go test ./...` → PASS with no test edits.
- **Step 5: Commit** `fix(i18n): localize handler error texts (#105)`.

### Task 3: Example file and format help

**Files:**
- Create: `internal/handler/question_example.go`, `internal/handler/question_example_test.go`,
  `internal/handler/views/question_format.templ`
- Modify: `internal/handler/handler.go` (route), `internal/handler/views/admin_questions.templ`,
  `internal/i18n/i18n.go`, `internal/i18n/middleware.go`, both locale files

**Interfaces:**
- Produces: `func questionExample(lang string) []model.QuestionImport` (two questions; `ru` in Russian, anything else
  English); `func (h *Handler) handleQuestionExample(w, r)`; route `GET /teacher/questions-example.json` in the
  teacher+admin group; templ `questionFormatHelp(example string)` where `example` is the indented JSON of
  `questionExample`, passed in by the caller.
- Produces in `internal/i18n`: `func WithLang(ctx context.Context, lang string) context.Context` and
  `func Lang(ctx context.Context) string` ("en" when unset); `Middleware(lang)` stores both the localizer and the
  language. The example handler uses `i18n.Lang(r.Context())`.

IDs: `QFormatTitle` "File format" / "Формат файла"; `QFormatShapes` (array or object with `questions`);
`QFormatColField`, `QFormatColRequired`, `QFormatColMeaning`; one `QFormatField<Name>` per field, meanings reuse the
constructor hints of Task 5; `QFormatDifficultyValues` ("easy, medium or hard"); `QFormatPointsRule` ("a whole number
of at least 1"); `QFormatDownload` "Download an example" / "Скачать пример".

- **Step 1: Write the failing tests**: `TestQuestionExampleValid` — for `ru` and `en`,
  `questionimport.Parse(json.MarshalIndent(questionExample(lang)))` returns 2 drafts and no errors;
  `TestQuestionExampleRoute` — a teacher GET returns 200, `Content-Type: application/json`,
  `Content-Disposition: attachment; filename=questions-example.json`, body parses; a student gets 403.
- **Step 2: Run** → FAIL.
- **Step 3: Implement** the example, handler, route and `questionFormatHelp` (closed `<details>`, field table,
  `<pre>{ example }</pre>`, download link via `p(ctx, "/teacher/questions-example.json")`); add it under the file
  input of `AdminQuestionsPage` (the admin handler passes the example JSON).
- **Step 4: Run** `templ generate && go test ./...` → PASS.
- **Step 5: Commit** `feat: question file format help and example file (#105)`.

### Task 4: Set names and the teacher profile

**Files:**
- Modify: `internal/handler/teacher.go` (`handleTeacherProfile`, save naming at `:398-406`, final redirect),
  `internal/handler/views/teacher_profile.templ`, `internal/handler/import_teacher_test.go:106`,
  `internal/handler/teacher_test.go:277`, both locale files
- Test: `internal/handler/teacher_test.go`

**Interfaces:**
- Produces: `func setName(file, username string) string` (strip `.json` and a leading `teacher_<username>_`);
  `views.QuestionSet{File, Name string}`; `views.SaveSummary{Name string; Inserted, Updated, Skipped int}`;
  `TeacherProfilePage(sets []QuestionSet, saved *SaveSummary)`.

IDs: `QSetListTitle` "My question sets" / "Мои наборы вопросов"; `QSetListIntro` (spec §3 text); `QSetCreate`
"Create a set" / "Создать набор"; `QSetEdit` "Edit" / "Редактировать"; `QSetDownload` "Download" / "Скачать";
`QSetEmpty` "You have no question sets yet. Create one by hand or upload a file." / "У вас пока нет наборов.
Создайте первый вручную или загрузите файл."; `QSetSaved` "Set «{{.Name}}» saved: {{.Inserted}} added,
{{.Updated}} updated, {{.Skipped}} skipped." / "Набор «{{.Name}}» сохранён: добавлено {{.Inserted}}, обновлено
{{.Updated}}, пропущено {{.Skipped}}.". Remove `TeacherUploadIntro` and the `flashMsg` parameter.

- **Step 1: Write the failing tests**:
  - `TestTeacherResaveKeepsFileName`: save `custom_filename=mid`, then save with `editing_file=teacher_teacher1_mid.json`
    and `custom_filename=mid` → `questionFiles(t)` is exactly `[teacher_teacher1_mid.json]`.
  - `TestTeacherOwnPrefixNotDoubled`: save with `custom_filename=teacher_teacher1_mid` → file
    `teacher_teacher1_mid.json`.
  - `TestTeacherSaveRedirectsWithSummary`: Location path `/teacher/profile`, query `saved=mid`, `inserted=2`,
    `updated=0`, `skipped=0` (parse with `url.Parse`).
  - `TestTeacherProfileShowsSetsAndSummary`: profile GET with `?saved=mid&inserted=2&updated=0&skipped=0` contains
    `Set «mid» saved: 2 added`, the set name `mid` (not `teacher_teacher1_mid`), an edit link and a link to
    `/teacher/tests/file/teacher_teacher1_mid.json`.
  - `TestTeacherProfileIgnoresBadSummary`: `?saved=<script>&inserted=x` → no `saved:` text and no `<script>`.
  - Update `import_teacher_test.go:106` and `teacher_test.go:277` to compare only the Location path
    (`/teacher/profile`, `/ru/teacher/profile`).
- **Step 2: Run** `go test ./internal/handler/ -run 'Teacher'` → the new tests FAIL.
- **Step 3: Implement**: in the save handler strip `.json` then a leading `teacher_<username>_` from
  `custom_filename` before building `safeName`; redirect to
  `h.path("/teacher/profile") + "?" + url.Values{saved, inserted, updated, skipped}.Encode()`; profile builds
  `[]QuestionSet` with `setName`, parses the summary with `strconv.Atoi` (any error → nil); rewrite
  `TeacherProfilePage` per spec §3 (green `role="status"` article; table of sets; empty state).
- **Step 4: Run** `templ generate && go test ./...` → PASS.
- **Step 5: Commit** `feat: teacher profile lists question sets; keep set names on save (#105)`.

### Task 5: Constructor page

**Files:**
- Modify: `internal/handler/views/teacher_create_test.templ`, `internal/handler/teacher.go`
  (`handleTeacherCreateTest`, `renderTeacherCreateTest` callers), `internal/handler/views/teacher_create_test_test.go`,
  both locale files

**Interfaces:**
- Consumes: `difficultyLabel` (Task 1), `questionFormatHelp`, `questionExample` (Task 3), `setName` (Task 4).
- Produces: `TeacherBuilder` gains `SetName string` (shown in the name field; replaces the filename slicing in the
  template) and `DefaultTopic string`; `func commonTopic(qs []views.BuilderQuestion) string` in `teacher.go`
  (the topic when all non-empty topics are equal, else ""); `TeacherBuilder.Example string` for the file tab.

IDs: `QSetNewTitle` "New question set" / "Новый набор вопросов"; `QSetEditTitle` "Set «{{.Name}}»" /
"Набор «{{.Name}}»"; `QSetBackToList` "← My sets" / "← Мои наборы"; `QSetName`, `QSetNameHint`,
`QSetNamePlaceholder`; `QSetDefaultTopic`, `QSetDefaultTopicHint`, `QSetDefaultTopicPlaceholder`; `QModeManual`
"Create by hand" / "Создать вручную"; `QModeFile` "Upload a file" / "Загрузить файл"; `QFileLabel` "Questions file
(JSON)" / "Файл с вопросами (JSON)"; `QBlockTitle` "Question" / "Вопрос"; `QBlockText`, `QBlockTextHint`;
`QBlockTopic`; `QBlockDifficulty`; `QBlockPoints`; `QBlockRubric`, `QBlockRubricHint`, `QBlockRubricPlaceholder`;
`QBlockAnswer`, `QBlockAnswerHint`; `QBlockRemove` "Remove question" / "Удалить вопрос"; `QBlockAdd`
"+ Add a question" / "+ Добавить вопрос"; `QSetSave` "Save the set" / "Сохранить набор". Hints use the copy of spec
§1. Change `QPublishLabel` per spec §1. Remove `Create Questions`, `Edit Questions`, `Exam Topic`, `Questions List`,
`Add Question`, `OrUploadJson`, `ApproveTest`, `TeacherProfile` from code and locales.

- **Step 1: Write the failing tests** in `teacher_create_test_test.go` (rename the existing test's assertions to
  the new markup):
  - `TestBuilderTemplateRendered`: `ru` render of a new page contains `<template id="question-template"`, inside it
    `Текст вопроса`, `Критерии оценки`, `Эталонный ответ`, `Удалить вопрос`, `<option value="easy">Лёгкий</option>`,
    and the names `q_text___IDX__` … `q_answer___IDX__`.
  - `TestBuilderModes`: new page has both mode tabs and `name="questions_file"` with `disabled`; edit page
    (`Filename` set) has no tabs and no `questions_file`.
  - `TestBuilderEditShowsSetName`: `Filename: "teacher_teacher1_mid.json", SetName: "mid"` → the name input value is
    `mid`, title `Набор «mid»`.
  - `TestBuilderKeepsDupForm`: with `Dups` and `Kept` set, the page still has `dup_token` and the hidden `dup_<N>`.
  - In `teacher_test.go`: `TestCommonTopic` (`["a","a"]`→`a`; `["a","b"]`→``; `["", "a"]`→`a`).
- **Step 2: Run** → FAIL.
- **Step 3: Implement** the page per spec §1: `questionBlock()` templ component inside
  `<template id="question-template">` with `__IDX__` placeholders; JS `addQuestion(data)` clones the template,
  replaces `__IDX__` in `name`/`id`/`for`, fills values, sets `data-qid`; default topic fills new blocks and empty
  topics on `change`; tabs toggle `hidden` on the two sections and `disabled`/`required` on the file input;
  `assembleAndSubmit` keeps today's collection logic and posted fields, skips collection in file mode, and replaces
  `alert` with `aria-invalid` marks plus an inline `role="alert"` box (texts from `data-msg-*`); removing the last
  block adds an empty one. Handler fills `SetName` (`setName(editingFile, user.Username)` when editing, else the posted
  `custom_filename`), `DefaultTopic` (`commonTopic`) and `Example`. Name input `required` for new sets too.
- **Step 4: Run** `templ generate && go test ./...` → PASS (including `duplicates_test.go`, `ownership_test.go`).
- **Step 5: Commit** `feat: clearer question constructor with translated question blocks (#105)`.

### Task 6: Guard tests for the whole Russian UI

**Files:**
- Create: `internal/handler/views/i18n_keys_test.go`, `internal/handler/views/ru_pages_test.go`

**Interfaces:**
- Consumes: everything above.

- **Step 1: Write `TestMessageIDsExist`**: walk `internal/handler` (`.templ` and non-test `.go`), collect literal
  IDs from `t(ctx, "…")`, `td(ctx, "…"`, `tp(ctx, "…"`, `appI18n.T(…, "…")`, `appI18n.Td(…, "…"`,
  `httpError(…, "…")`; fail listing every ID missing from `active.en.json` or `active.ru.json`.
- **Step 2: Mutation check**: temporarily delete `QBlockRemove` from `active.ru.json`, run the test → FAIL naming
  it; restore.
- **Step 3: Write `TestRussianPagesHaveNoEnglishLabels`**: render with `ru`: `TeacherCreateTestPage` (new; edit
  with `Dups`), `TeacherProfilePage` (sets and summary), `AdminQuestionsPage`, `Layout` with a user of each role,
  `ResultsPage` for a `graded` session with a `medium` question; assert none of: `Question text`, `Difficulty`,
  `>easy<`, `>medium<`, `Remove`, `>Edit<`, `Create Questions`, `Exam Topic`, `TeacherProfile`, `graded<`,
  `in_progress`, `(student)`, `(teacher)`, `(admin)`, `Follow-up question`, `Time's up`.
- **Step 4: Run** `templ generate && go test ./... && go vet ./...` and golangci-lint → PASS, 0 issues.
- **Step 5: Commit** `test: guard message IDs and the Russian UI (#105)`.

### Task 7: Manual check, docs, PR

**Files:**
- Modify: `README.md` (teacher section, if it names old labels or the `teacher_<username>_` file naming), spec status

- **Step 1:** Build, run `--lang ru` with the LLM stub on a fresh DB; walk spec §7: create a set by hand (with a
  missed field), upload a file with an error and fix it, edit the set twice (file name unchanged), duplicate dialog,
  download the example and a set; admin: questions, users, backups; student: exam and results. Note any English seen.
- **Step 2:** Update README where it describes the constructor; commit
  `docs: describe the new question constructor (#105)`.
- **Step 3:** Push the branch and open a PR "Closes #105" with what changed, the bug found (double prefix), test
  evidence and the manual check result.
