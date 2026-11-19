# Question Constructor UI and Full Russian Localization Design (#105)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:writing-plans to create the implementation plan after
> this spec is approved.

**Goal:** A teacher who has never seen Examiner creates a question set by hand and uploads one from a file, guided only
by the labels and hints on the page. The Russian UI (`--lang ru`) shows no English, except user data and technical
identifiers.

**Architecture:** The question block is rendered once by templ inside a `<template>` element, with every label from
`t(ctx, …)`. JavaScript only clones it, fills values and assembles the JSON it already posts. Shared label helpers
translate session statuses, roles and difficulty everywhere. A small handler helper sends localized `http.Error` texts.

**Tech Stack:** Go 1.25, templ, go-i18n, htmx 2 and Pico CSS v2. No new dependencies. Builds on `main` at `f469ba4` (#79
merged).

---

## Why

The teacher pages were built for developers. On the Russian UI a teacher sees raw message IDs (`Create Questions`, `Exam
Topic`), an English question form (`Question text`, `Difficulty: easy`, `Rubric (grading criteria for LLM)`), and a
field called "exam topic" that actually names the file. A JSON upload hidden in the same form silently discards the
questions typed above it. Outside the question pages, Russian users see `Статус: graded`, `(student)`, `medium` and
English error texts.

## Goals

- Every visible string on the Russian UI comes from `active.ru.json`.
- The constructor explains each field in plain words: what it is, who sees it, how it is used.
- Creating by hand and uploading a file are two clearly separate ways, never mixed in one submit.
- The file format is described on the page, with an example file to download.
- A successful save is confirmed; warnings are not shown as errors.
- A test fails when a template or handler uses a message ID that is missing from either locale.

## Non-goals

- The `grader` app (`cmd/grader`, `internal/grader`): English-only, no i18n. Separate issue.
- Question bank UI ("My questions" / "Shared bank", admin question list): #44 stages B and C.
- Changes to the JSON format, file naming (`teacher_<username>_<name>.json`), storage, validation rules or duplicate
  logic (#42, #43, #79).
- Translating user data (question text, topic names, display names) or log messages.

## Anti-goals

- Breaking the #79 flow: `question_ids`, the duplicate form with `dup_token`, kept decisions and `editing_file` must
  work exactly as now. All tests in `duplicates_test.go`, `ownership_test.go`, `import_teacher_test.go` and
  `teacher_test.go` keep passing.
- A page that works only with JavaScript in a way it does not today. The constructor already needs JS; the profile,
  admin page and upload remain plain forms where they are now.
- English UI regressions: the English locale gets the same clarity, not leftovers.

## Constraints

- Templates are templ; messages are go-i18n JSON files (`internal/i18n/locales/active.{en,ru}.json`). `t`, `td`, `tp` in
  views; `appI18n.T`/`Td` in handlers. A missing key renders the key itself and logs a warning.
- Handler tests run with the English locale and assert English strings (e.g. `"Question 1 matches your question #1"`,
  `"nothing to save"`, `"already been imported"`). Existing message texts that tests assert stay as they are in English.
- All links and form actions go through `p(ctx, …)` / `h.path(…)` for `--base-path /ru`.
- Inline `<script>` and `<style>` are already used; no CSP to satisfy.
- `validQuestionFileName` allows Cyrillic and spaces; it rejects `< > : " / \ | ? *` and control characters, up to 255
  bytes for the full `teacher_<username>_<name>.json`.

## Approaches considered

1. **Server-rendered block template (chosen).** templ renders one question block inside `<template
   id="question-template">` with all labels translated. JS clones it, sets `name` suffixes and values. One source of
   text, both languages for free, the markup stays readable.
2. **Translation dictionary for JS.** Keep building blocks in JS strings and pass translations as a JSON object. Works,
   but markup stays in JS strings and every new label must be threaded through the dictionary.
3. **htmx fragments per block.** Each "Add question" fetches a block from the server. Extra requests, and keeping typed
   values across an error re-render gets harder. Rejected.

The user chose approach 1 and asked to build from `main`.

---

## Design

### 1. Constructor page (`/teacher/create-test`, `TeacherCreateTestPage`)

Layout (Russian copy shown; English equivalents in the locale file):

```
Новый набор вопросов                                         ← Мои наборы
[duplicate choices from #79, unchanged, when present]
[errors, when present]

Название набора  [Механика, 1 курс                    ]
  Так набор называется в вашем списке. Студенты его не видят.
Тема по умолчанию  [Законы Ньютона                     ]
  Подставляется в новые вопросы. У каждого вопроса тему можно изменить.
  Экзамен собирается из вопросов одной темы.

( Создать вручную ) ( Загрузить файл )         ← only for a new set

── Создать вручную ─────────────────────────────────────────────
Вопрос 1                                              [Удалить вопрос]
  Текст вопроса            [textarea, 3 rows]
    Так вопрос увидит студент.
  Тема                     [input]      Сложность [Лёгкий ▾]   Баллы [5]
  Критерии оценки          [textarea]
    Что должно быть в ответе и сколько баллов за каждый пункт.
    По этим критериям ИИ оценивает ответ. Студенты их не видят.
  Эталонный ответ          [textarea]
    Полный правильный ответ. ИИ сравнивает с ним ответ студента.
    Студенты его не видят.
[+ Добавить вопрос]

── Загрузить файл ──────────────────────────────────────────────
  Файл с вопросами (JSON)  [Выбрать файл]
  ▸ Формат файла          (details: field table, example, download link)

[x] Опубликовать: вопросы попадут в экзамены, другие преподаватели
    увидят их, но не смогут изменить
[Сохранить набор]
```

- **Titles.** New: `QSetNewTitle` ("Новый набор вопросов"). Edit: `QSetEditTitle` with `{{.Name}}` ("Набор
  «{{.Name}}»"). Page `<title>` uses the same keys. The back link is `QSetBackToList` ("← Мои наборы").
- **Set name** (`custom_filename`, unchanged field name). Label `QSetName`, hint `QSetNameHint`, placeholder
  `QSetNamePlaceholder`. Required for a new set too (today it is optional and falls back to a Unix timestamp name nobody
  can recognize). The server fallback stays for API callers.
- **Default topic.** New input `default_topic`, not used by the server. Label, hint, placeholder from
  `QSetDefaultTopic*`. JS copies its value into the topic of every block added afterwards and into empty topic fields
  when the user leaves the field. On the edit page it is prefilled when every question has the same topic; otherwise
  empty. The handler computes this (`commonTopic(questions)`), so the template stays simple.
- **Mode tabs** (new set only): two buttons with `role="tab"`, `aria-selected`, styled as a Pico button group. The
  active tab shows its section and hides the other with the `hidden` attribute.
  - Manual mode: the file input is disabled (so a stale file selection is never posted) and blocks are validated as
    today.
  - File mode: the file input is `required`; `questions_json` is left empty and blocks are not validated or posted. The
    handler already prefers `questions_file` when present.
  - After a failed file upload the page re-renders in manual mode with the questions read from the file prefilled
    (current behavior), so the teacher can fix them in the form.
  - Edit page: no tabs, manual mode only (as today: no file input when editing).
- **Question block** (`<template id="question-template">`, rendered once by a templ component `questionBlock()`):
  - Fields keep their current names with the `_${idx}` suffix (`q_text_`, `q_topic_`, `q_diff_`, `q_points_`,
    `q_rubric_`, `q_answer_`), so `assembleAndSubmit` and the posted JSON do not change. In the template the names carry
    a placeholder suffix (`__IDX__`) that JS replaces when it clones the block. Labels use `for`/`id` with the same
    suffix.
  - Question text becomes a `<textarea rows="3">` (questions are often longer than one line).
  - Difficulty options: values `easy|medium|hard`, labels from `DifficultyEasy|Medium|Hard` ("Лёгкий", "Средний",
    "Сложный").
  - Points default 5 (was 1; 1 is never a useful maximum for a rubric with several items). `min="1" step="1"`.
  - Block heading: localized prefix `QBlockTitle` ("Вопрос") followed by the CSS counter number.
  - Remove button: `QBlockRemove` ("Удалить вопрос"). Removing the last block leaves one empty block.
  - Hints below fields use `<small>` (Pico renders it muted).
- **Client validation.** Instead of `alert()`, `assembleAndSubmit` marks each missing field of a started block with
  `aria-invalid="true"` (Pico shows it red), clears the mark on input, scrolls to the first one and shows the existing
  `QBuilderIncomplete` / `QBuilderEmpty` text in an inline `role="alert"` box above the submit button. The rule for a
  "started" block stays: text or topic filled.
- **Publish checkbox:** `QPublishLabel` gets clearer copy: "Опубликовать: вопросы попадут в экзамены, другие
  преподаватели увидят их, но не смогут изменить". The key is kept; tests do not assert its text.
- **Submit:** `QSetSave` ("Сохранить набор") replaces `ApproveTest`.
- **Unchanged:** `editing_file`, `question_ids` assembly from `data-qid`, `preloaded-data`, the duplicate choices
  component and kept decisions from #79.

### 2. File format help and example file

- A templ component `questionFormatHelp()` renders a `<details>` block, closed by default, titled `QFormatTitle`
  ("Формат файла"). It holds:
  - one sentence on the two accepted shapes (an array of questions, or an object with a `questions` array);
  - a table: field, required, meaning. The six fields: `text`, `topic`, `difficulty` (`easy`, `medium` or `hard`),
    `max_points` (a whole number of at least 1), `rubric`, `model_answer`. Meanings reuse the hints of the constructor
    fields;
  - a `<pre>` with a two-question example;
  - a link `QFormatDownload` ("Скачать пример") to the example file.
- The component is used on the constructor's file tab and on `/admin/questions`.
- **Example file:** new route `GET /teacher/questions-example.json` in the teacher+admin route group
  (`requireRole(teacher, admin)`, next to `/teacher/tests/file/{name}`), so the admin page can link to it too. Handler
  `handleQuestionExample` serves the example for the request language as `application/json` with `Content-Disposition:
  attachment; filename=questions-example.json`. Content: a Go function `questionExample(lang string)
  []model.QuestionImport` with two questions per language (Russian physics for `ru`, English for `en`); the `<pre>` on
  the page renders the same value, so they cannot drift.
- A test parses both examples with `questionimport.Parse` and requires zero errors.

### 3. Teacher profile (`/teacher/profile`, `TeacherProfilePage`)

```
Мои наборы вопросов
  Набор — это группа ваших вопросов. В экзамен попадают опубликованные
  вопросы выбранной студентом темы, из всех наборов.
[Набор «Механика, 1 курс» сохранён: добавлено 3, обновлено 1.]   ← green, once
[+ Создать набор]

| Набор              |                              |
| Механика, 1 курс   | [Редактировать] [Скачать]    |

(empty: "У вас пока нет наборов. Создайте первый вручную или загрузите файл.")
```

- Page title and `<h1>`: `QSetListTitle` ("Мои наборы вопросов"). The teacher's name already shows in the header.
- Intro: `QSetListIntro` (above). Replaces `TeacherUploadIntro`, which has a typo ("созданный") and describes the old
  flow.
- Each set: name without the `teacher_<username>_` prefix and `.json` (today the prefix is shown; strip it with the
  existing owner rule, falling back to the full name). Actions: `QSetEdit` ("Редактировать") →
  `/teacher/create-test?file=…`, `QSetDownload` ("Скачать") → existing `/teacher/tests/file/{name}`.
- **Success message.** After a save the handler redirects to `/teacher/profile?saved=<set
  name>&inserted=N&updated=M&skipped=K` (currently a bare redirect). The page shows `QSetSaved` with those numbers in a
  green `role="status"` box. Values are parsed as integers; invalid ones hide the message. The name is shown as text
  (templ escapes it).
- The `flashMsg` parameter and its red styling are removed: after #79 the handler no longer renders the profile with
  warnings.

### 4. Admin question import (`/admin/questions`)

- Add `questionFormatHelp()` under the file input.
- Keep labels, the `on_duplicate` select and the stored-duplicates report as they are (already translated).
- `AdminQuestions` stays "Загрузка вопросов".

### 5. Localization across the UI

**Missing and hard-coded text.**

| Where | Today | Fix |
| --- | --- | --- |
| `teacher_create_test.templ`, `teacher_profile.templ` | IDs without translations: `Create Questions`, `Edit Questions`, `Exam Topic`, `Questions List`, `Add Question`, `Your Questions List`, `TeacherProfile`; JS block text; `Edit` | Replaced by the new `QSet*`, `QBlock*`, `QFormat*` keys above; unused IDs removed from code |
| `layout.templ` | `({ string(user.Role) })` | `roleLabel(ctx, user.Role)` |
| `admin_users.templ` | role column `string(u.Role)`; role options `student/teacher/admin` | `roleLabel` |
| `evaluator_message.templ` | `Follow-up question:` | new key `FollowupQuestion` |
| `exam.templ` | `Time's up! Please submit your exam.` (static text in the timer section) | new key `TimeUp` |
| `results.templ`, `review.templ`, `thread.templ` | `string(…Difficulty)` | `difficultyLabel(ctx, d)` |
| `index.templ` | `d.Config.Difficulty` (comma-separated filter) | each value through `difficultyLabel`, joined with ", " |

**Label helpers** (`internal/handler/views/labels.go`, plain Go in the views package):

- `sessionStatusLabel(ctx, status)` moves here from `session_reset.templ` and covers all seven statuses: `in_progress`,
  `submitted`, `grading`, `grading_failed`, `graded`, `reviewed`, `annulled`. It reuses existing keys where they say the
  right thing (`SessionGradingFailed`, `AttemptAnnulled`, `StatementSubmitted`, `StatementGrading`, `StatementGraded`,
  `StatementReviewed`) and adds `StatusInProgress`. Unknown values fall back to the raw value (data, not UI).
- `roleLabel(ctx, role)`: new keys `RoleStudent`, `RoleTeacher`, `RoleAdmin` ("Студент", "Преподаватель",
  "Администратор"). The existing `Admin` key is the nav item "Администрирование" and `Student` is a table column;
  neither is reused.
- `difficultyLabel(ctx, d)`: `DifficultyEasy|Medium|Hard`.
- `threadStatusLabel` and `gradingStateLabel` stay where they are; they already cover their values.

**Handler error texts.** `http.Error(w, "<english>", status)` reaches users as the body of htmx error responses (shown
in the alert box from `layout.templ`) and as plain error pages.

- New helper in `internal/handler`: `httpError(w http.ResponseWriter, r *http.Request, status int, msgID string)` →
  `http.Error(w, appI18n.T(r.Context(), msgID), status)`.
- Generic keys: `ErrInternal` ("Внутренняя ошибка. Попробуйте ещё раз; если не поможет, сообщите администратору."),
  `ErrForbidden` ("Недостаточно прав."), `ErrUnauthorized` ("Войдите в систему."), `ErrNotFound` ("Не найдено."),
  `ErrBadRequest` ("Некорректный запрос."). Specific keys for the messages users can trigger by input: username taken /
  required, invalid or out-of-range score and grade, session not awaiting review, file too large, no file uploaded, no
  input provided, invalid file name, invalid export format.
- Replace every `http.Error` with a literal string in `internal/handler/*.go` (non-test). The English texts of the
  generic keys stay the same lowercase words the tests may assert (`"internal error"`, `"forbidden"`, `"not found"`, …):
  keep the English values identical to today's literals so existing tests do not change; only Russian gets full
  sentences.
- `parseIDParam`'s `"invalid "+name` becomes `ErrBadRequest`.
- `/healthz` keeps its plain-text `ok` / status texts: it is for monitoring, not people.

### 6. Tests

- **Message IDs exist in both locales** (`internal/handler/views/i18n_keys_test.go`): scan `internal/handler/**/*.templ`
  and non-test `internal/handler/*.go` for literal IDs in `t(`, `td(`, `tp(`, `appI18n.T(`, `appI18n.Td(`, `httpError(…,
  "<id>")`, and fail on any ID missing from `active.en.json` or `active.ru.json`. This would have caught all seven raw
  IDs.
- **Russian pages without English labels** (views render tests with a Russian localizer): render `TeacherCreateTestPage`
  (new and edit), `TeacherProfilePage`, `AdminQuestionsPage`, a results page and the layout with each role, and assert
  the absence of a list of known English strings (`Question text`, `Difficulty`, `>easy<`, `Remove`, `>Edit<`, `Create
  Questions`, `graded`, `in_progress`, `(student)`, `Follow-up question`). Also assert that the question `<template>`
  exists and contains the Russian labels.
- **Labels:** table tests for `sessionStatusLabel` (all seven), `roleLabel`, `difficultyLabel` in both languages.
- **Profile success message:** handler test: a save redirects with `saved`/counts; the profile renders the message;
  garbage counts render no message.
- **Example file:** both languages pass `questionimport.Parse`; the route serves JSON with the attachment header and
  requires login.
- **Existing tests** keep passing unchanged where possible. Two tests assert the exact redirect target of a save and
  must accept the new query string: `import_teacher_test.go:106` (`/teacher/profile`) and `teacher_test.go:277`
  (`/ru/teacher/profile`, base path). Check the path and the `saved` parameter instead. Tests that assert removed text
  (`teacher_create_test_test.go`) are updated to the new keys.

### 7. Manual check

Run `task run -- --lang ru` with an LLM stub, then as a new teacher: create a set by hand (including a validation miss),
upload a file with an error and fix it, edit a set, trigger the duplicate dialog, download the example and a set; as
admin: upload a file, open users and backups; as a student: take an exam and open results. No English on any page.

## Left to the builder

- Exact Russian and English copy for hints, within the meaning above; keep it short.
- Visual details within Pico defaults (spacing, the tab group look). Prefer Pico classes over inline styles; remove
  inline styles the redesign touches.
- Whether `labels.go` or a templ file hosts the helpers.
- Order of commits.
