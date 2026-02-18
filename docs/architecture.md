# Architecture

This document describes the internal design of Examiner.
For quick-start instructions, see the project `README.md`.

## System overview

Examiner is a server-side rendered web application.
The browser communicates with the Go server over HTTP;
the server calls an external LLM via the OpenAI chat completion API.

```text
┌─────────┐     HTTP      ┌──────────────┐   OpenAI API   ┌─────────┐
│ Browser  │◄────────────►│  Go server   │◄──────────────►│   LLM   │
│ (htmx)  │               │  (chi + templ)│               │ (Ollama │
└─────────┘               └──────┬───────┘               │  / API) │
                                 │                        └─────────┘
                                 │ SQLite
                                 ▼
                          ┌──────────────┐
                          │ examiner.db  │
                          └──────────────┘
```

There is no client-side JavaScript framework.
Dynamic updates (submitting answers, receiving LLM feedback)
use htmx with HTML fragment swaps.

## Package structure

```text
cmd/examiner/main.go      Application entry point
internal/
  handler/handler.go       HTTP handlers and routing
  handler/views/*.templ    Templ components (UI layer)
  i18n/i18n.go             Translation helpers (T, Td, Tp)
  i18n/middleware.go        HTTP middleware injecting localizer into context
  i18n/locales/*.json      Translation files
  llm/llm.go               LLM client (evaluate, grade)
  model/model.go           Domain types
  store/store.go           SQLite persistence and migrations
```

### Dependency graph

```text
cmd/examiner
  ├── handler
  │     ├── views  (Templ components)
  │     │     └── i18n
  │     ├── model
  │     ├── store
  │     └── llm
  ├── i18n
  ├── store
  └── llm
        └── model
```

Key rule: `model` has zero internal dependencies.
`store`, `llm`, and `i18n` depend only on `model` (or nothing).
`handler` ties everything together.

## Exam lifecycle

An exam session moves through these statuses:

```text
in_progress ──► submitted ──► grading ──► graded ──► reviewed
```

Each question within a session has its own thread:

```text
open ──► answered ──► completed
         (may cycle if LLM asks follow-ups)
```

### Detailed flow

1. **Start exam** (`POST /exam/start`):
   the server creates an `ExamSession` and one `QuestionThread`
   per question, all with status `open`.

1. **Answer a question** (`POST /exam/{id}/answer/{threadID}`):
   the student submits text. The server saves it as a `Message`
   (role=student), then calls `llm.EvaluateAnswer()`.
   The LLM returns JSON with `feedback`, `score`,
   and optionally a `followup_question`.
   The LLM response is saved as a `Message` (role=assistant).
   If a follow-up was asked, the thread stays `answered`;
   otherwise it becomes `completed`.
   The handler returns an HTML fragment (Templ `ThreadContent`
   component) for htmx to swap into the page.

1. **Submit exam** (`POST /exam/{id}/submit`):
   status changes to `grading`. For each thread,
   the server calls `llm.GradeThread()` which reviews the full
   conversation and produces a final score.
   Scores are saved to `question_scores`.
   An overall percentage grade is computed and saved to `grades`.
   Status changes to `graded`. The user is redirected to the
   review page.

1. **Teacher review** (`GET /review/{id}`):
   the teacher sees all questions, conversations, LLM scores,
   and LLM feedback. They can adjust individual scores
   (`POST /review/{id}/score/{threadID}`)
   and finalize the grade (`POST /review/{id}/finalize`).

## Database schema

SQLite with WAL mode. Schema is auto-migrated on startup.

### Tables

| Table | Purpose | Key columns |
| ----- | ------- | ----------- |
| `questions` | Question bank | `text`, `difficulty`, `topic`, `rubric`, `model_answer`, `max_points` |
| `exam_blueprints` | Exam configuration | `name`, `time_limit`, `max_followups` |
| `exam_sessions` | One per exam attempt | `blueprint_id`, `status`, `started_at`, `submitted_at` |
| `question_threads` | One per question per session | `session_id`, `question_id`, `status` |
| `messages` | Conversation messages | `thread_id`, `role`, `content`, `created_at` |
| `question_scores` | Per-question scores | `thread_id`, `llm_score`, `llm_feedback`, `teacher_score` |
| `grades` | Per-session grades | `session_id`, `llm_grade`, `final_grade` |

### Relationships

```text
exam_blueprints  1──*  exam_sessions  1──*  question_threads  1──*  messages
                                             │
questions  1─────────────────────────────────┘
                                             │
                                    question_scores  1──1  question_threads
                                    grades           1──1  exam_sessions
```

## LLM integration

The `llm` package uses the `sashabaranov/go-openai` library
to talk to any OpenAI-compatible endpoint.

### Two LLM calls per question

1. **EvaluateAnswer** — called each time a student submits an answer.
   System prompt includes the question, rubric, and model answer.
   The LLM responds with JSON: score, feedback, and whether to ask
   a follow-up. Temperature: 0.3.

1. **GradeThread** — called once during exam submission.
   Reviews the full conversation and produces a final score.
   Temperature: 0.1 (more deterministic for grading).

### Follow-up logic

The blueprint's `max_followups` field controls how many follow-up
questions the LLM may ask per thread. The evaluator prompt changes
based on whether follow-ups remain:

- If under the limit: LLM *may* ask a follow-up if the answer
  is incomplete or ambiguous
- If at the limit: prompt explicitly instructs the LLM
  not to ask further questions

## Templating with Templ

The UI layer uses [Templ](https://templ.guide/) — a typed
HTML templating language for Go. `.templ` files compile to
Go code via `templ generate`.

### Component hierarchy

```text
Layout (HTML shell, CSS, htmx script)
├── IndexPage         Home page, session list
├── ExamPage          Exam view with all threads
│   └── ThreadContent   Single question thread (htmx fragment)
├── ReviewListPage    List of graded sessions
└── ReviewPage        Detailed review with scoring forms
```

`ThreadContent` serves double duty: it renders inside `ExamPage`
on initial load, and it is returned as a standalone HTML fragment
for htmx swap after answering a question.

### Why Templ over html/template

- Compile-time type checking (no runtime `map[string]any` bags)
- Components are Go functions with typed parameters
- `ctx context.Context` flows naturally through the component tree
  (used by i18n)
- No `FuncMap` or `printf "%s"` type assertions needed

## Internationalization

The i18n layer uses [go-i18n](https://github.com/nicksnyder/go-i18n)
v2 with CLDR plural rules.

### How it works

1. On startup, `i18n.Init(lang)` loads all `active.*.json` files
   from the embedded `locales/` directory into a `Bundle`.

1. `i18n.Middleware(lang)` creates a `Localizer` for the configured
   language and stores it in every request's `context.Context`.

1. Templ components call helper functions to translate strings:

   | Function | Use case | Example |
   | -------- | -------- | ------- |
   | `t(ctx, id)` | Simple string | `t(ctx, "AppTitle")` → "Examiner" |
   | `td(ctx, id, data)` | String with variables | `td(ctx, "SessionN", {"ID": "5"})` → "Session #5" |
   | `tp(ctx, id, count)` | Pluralized string | `tp(ctx, "QuestionsLoaded", 5)` → "5 questions loaded." |

1. If a key is missing in the active language, `go-i18n` falls back
   to the default language (English).

### Adding a language

1. Copy `internal/i18n/locales/active.en.json` to `active.XX.json`
1. Translate each entry's `"other"` value
   (and add plural forms if the language requires them)
1. Rebuild and run with `--lang XX`

### Russian plural forms

Russian has four CLDR plural categories:

| Category | Rule | Example |
| -------- | ---- | ------- |
| one | ends in 1, not 11 | 1 вопрос |
| few | ends in 2-4, not 12-14 | 2 вопроса |
| many | ends in 0, 5-9, 11-14 | 5 вопросов |
| other | fractional numbers | 1,5 вопроса |

## HTTP routes

| Method | Path | Handler | Description |
| ------ | ---- | ------- | ----------- |
| GET | `/` | `handleIndex` | Home page |
| POST | `/exam/start` | `handleStartExam` | Create new session |
| GET | `/exam/{sessionID}` | `handleExamPage` | Exam page |
| POST | `/exam/{sessionID}/answer/{threadID}` | `handleAnswer` | Submit answer (htmx) |
| POST | `/exam/{sessionID}/submit` | `handleSubmit` | Submit exam for grading |
| GET | `/review` | `handleReviewList` | Review dashboard |
| GET | `/review/{sessionID}` | `handleReviewPage` | Review a session |
| POST | `/review/{sessionID}/score/{threadID}` | `handleUpdateScore` | Adjust score |
| POST | `/review/{sessionID}/finalize` | `handleFinalize` | Finalize grade |

## Frontend stack

- **Pico CSS v2** — classless CSS framework for clean default styling
- **htmx 2.0** — HTML-over-the-wire for dynamic updates
  without a JavaScript build step

The only JavaScript on the page is htmx and a small
`confirm()` dialog on exam submission.

## Configuration

### Exam parameters

The `ExamConfig` struct (defined in `model/model.go`) controls
how each exam session is assembled:

| Field | CLI flag | Effect |
| ----- | -------- | ------ |
| `NumQuestions` | `--num-questions` | Limit questions per exam (0 = all) |
| `Difficulty` | `--difficulty` | Filter question bank by difficulty |
| `Topic` | `--topic` | Filter question bank by topic |
| `MaxFollowups` | `--max-followups` | Cap follow-up questions per thread |
| `Shuffle` | `--shuffle` | Randomize question selection and order |

When `handleStartExam` is called, it:

1. Queries `ListQuestionsFiltered(difficulty, topic)` from the store
1. Shuffles the result if `--shuffle` is set
1. Truncates to `NumQuestions` if set and less than available
1. Creates the session with only the selected question IDs

The `MaxFollowups` value is written into the exam blueprint
at question-load time and checked during `EvaluateAnswer` calls.

### Configuration stack (pflag + Viper)

Configuration uses [pflag](https://github.com/spf13/pflag) for
POSIX-compliant flags and [Viper](https://github.com/spf13/viper)
for unified configuration from multiple sources.

**Precedence** (highest to lowest):

1. CLI flags (`--lang ru`)
1. Environment variables (`EXAMINER_LANG=ru`)
1. Config file (`examiner.yaml`)
1. Default values

All flags are defined in `init()`, then bound to Viper
via `viper.BindPFlags()`. Viper's `SetEnvPrefix("EXAMINER")`
and `AutomaticEnv()` handle env var mapping automatically —
hyphens in flag names become underscores
(`--llm-url` → `EXAMINER_LLM_URL`).

Config file is optional. Viper searches for `examiner.yaml`
(or `.toml`, `.json`) in `.`, `~/.config/examiner/`,
and `/etc/examiner/`.

### Language

Language is selected once at startup via `--lang` / `-l` flag,
`EXAMINER_LANG` env var, or config file.
There is no runtime language switching — the same localizer
is injected into every request.

### LLM connection

LLM connection parameters can be set via flags, env vars,
or config file (see `README.md` for the full table).
