# Grader

Standalone teacher grading app for reviewing and grading exam results
exported from the examiner.

Teachers import exam result JSONs, review each student's answers
alongside LLM scores, override grades with their own scores and
comments, and download per-student Markdown reports.

## Quick start

### Build

```bash
task grader-build
```

### Import exam results

```bash
./bin/grader import results1.json results2.json --db grader.db
```

### Start the server

```bash
GRADER_ADMIN_PASSWORD=secret ./bin/grader serve --db grader.db --port 8082
```

Open `http://localhost:8082` and log in as `admin`.

## Configuration

All flags can be set via environment variables with the `GRADER_`
prefix (e.g. `GRADER_DB`, `GRADER_PORT`).

| Flag | Env var | Default | Description |
| ---- | ------- | ------- | ----------- |
| `--db` | `GRADER_DB` | `grader.db` | SQLite database path |
| `--port` | `GRADER_PORT` | `8082` | HTTP listen port |
| `--admin-password` | `GRADER_ADMIN_PASSWORD` | (required on first run) | Initial admin password |
| `--secure-cookies` | `GRADER_SECURE_COOKIES` | `true` | Set Secure flag on cookies (disable for local dev) |
| `--log-level` | `GRADER_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `--log-format` | `GRADER_LOG_FORMAT` | `text` | Log format (text, json) |

## Workflow

1. **Import** — admin imports exam result JSONs via CLI or web upload
1. **Review** — teacher opens an exam, selects a student, reviews
   each question's conversation and LLM score
1. **Grade** — teacher overrides scores per question, adds comments,
   then finalizes with an overall grade and comment
1. **Report** — teacher downloads a Markdown report for the student

## User roles

| Role | Capabilities |
| ---- | ------------ |
| admin | Upload JSONs, manage users, delete exams, review and grade |
| teacher | Review and grade exams, download reports |

The admin account is created automatically on first run using the
provided password. Additional teacher accounts are created through
the admin UI at `/admin/users`.

## CLI commands

### `grader serve`

Starts the HTTP server. This is the default command (runs when no
subcommand is given).

### `grader import [files...]`

Imports one or more exam result JSON files into the database. Each
file must match the `ExamExport` format produced by the examiner's
`export` command. Duplicate exam IDs are rejected.

## Input format

The grader consumes the same JSON format that `examiner export`
produces. See `examples/exam-2026-03-07/` for samples.

```json
{
  "exam_id": "phys-2026-spring-g1",
  "subject": "Physics",
  "date": "2026-03-15",
  "results": [
    {
      "external_id": "STU-001",
      "display_name": "Alice Johnson",
      "questions": [
        {
          "text": "...",
          "conversation": [...],
          "llm_score": 7,
          "llm_feedback": "..."
        }
      ],
      "llm_grade": 92.7
    }
  ]
}
```

## Tech stack

- Go, chi router, htmx 2.0, Pico CSS v2
- SQLite (modernc.org, pure Go, no CGO)
- Templ for HTML templates
- Cobra + Viper for CLI
