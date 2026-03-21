# Teacher Grading App (Grader) Design Spec

## Overview

A standalone web application for teachers to review and grade student
exam results. The grader imports JSON result files exported from the
examiner app, provides a web UI for per-student review, and produces
Markdown reports.

The grader lives in the same repository as the examiner
(`cmd/grader/`) but builds as a separate binary with its own database.

## Goals

- Import exam result JSONs (CLI and web upload)
- Per-student grading workflow with teacher score overrides and comments
- Multi-exam support (multiple imported exams in one instance)
- Markdown report generation per student
- Role-based access (admin, teacher)

## Non-Goals (Roadmap)

- PDF report generation
- Per-question analytics (identify problematic questions)
- Teacher-exam assignment (restrict grading access per exam)
- Bulk report export (zip of all student reports)
- LLM re-grading from within the grader
- Multi-teacher support with session locking (prevent two teachers
  from grading the same student simultaneously)

## Project Structure

```text
cmd/grader/                  -- entry point, CLI (import + serve)
internal/grader/
  handler/                   -- HTTP handlers (chi router)
  handler/views/             -- Templ templates
  store/                     -- SQLite storage (own schema)
  report/                    -- Markdown report generation
internal/model/export.go     -- shared types (ExamExport, etc.)
```

The grader packages live under `internal/grader/` to avoid import path
conflicts with the examiner's `internal/handler/` and `internal/store/`.

## CLI Interface

```bash
# Import result JSONs into the database
grader import file1.json file2.json --db grader.db

# Start the web server
grader serve --db grader.db --port 8082
```

The `import` subcommand parses each JSON, inserts data, prints a
summary (exam ID, student count), and exits. The `serve` subcommand
starts the HTTP server.

## Database Schema

SQLite database with these tables:

| Table                   | Key Columns                                                                             | Purpose                                                                                            |
| ----------------------- | --------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| `users`                 | id, username, display_name, password_hash, role, active                                 | Admin and teacher accounts                                                                         |
| `auth_sessions`         | id, user_id, created_at, expires_at                                                     | Login sessions                                                                                     |
| `exams`                 | id, exam_id (UNIQUE), subject, date, prompt_variant, num_questions, imported_at         | Imported exam metadata                                                                             |
| `students`              | id, exam_db_id, external_id, display_name                                               | Students scoped per exam                                                                           |
| `exam_sessions`         | id, exam_db_id, student_id, session_number, status, started_at, submitted_at, llm_grade | One per student per exam (session_number from JSON)                                                |
| `questions`             | id, session_id, position, text, topic, difficulty, max_points, rubric, model_answer     | Per-session questions                                                                              |
| `conversation_messages` | id, question_id, role, content, timestamp                                               | Messages per question                                                                              |
| `scores`                | id, question_id, llm_score, llm_feedback, teacher_score, teacher_comment                | Per-question grading (created at import with NULL teacher fields; `handleUpdateScore` does UPDATE) |
| `grades`                | id, session_id, final_grade, teacher_comment, reviewed_by, reviewed_at                  | Per-session final grade (`reviewed_by` is FK to `users.id`)                                        |

### Session Statuses

`imported` -> `in_review` -> `reviewed`

- **imported**: result JSON loaded, no teacher action yet
- **in_review**: teacher has started reviewing (set on first score save)
- **reviewed**: teacher has finalized the grade

### Route Parameters

`{examID}` in URLs refers to the string `exam_id` (e.g.
`phys-2026-spring-g1`), not the auto-increment DB primary key.

### Grade Scale

All grades are percentages (0.0-100.0). The `llm_grade` is imported
from JSON as-is. The `final_grade` is entered by the teacher as a
percentage. The teacher score per question must be between 0 and the
question's `max_points`.

### Concurrency

Concurrent edits use last-write-wins semantics. This is acceptable
for the expected scale (small number of teachers).

### Session Expiry

Auth sessions expire after 24 hours. Each per-question save is
independent (htmx POST), so only unsaved form state is lost on expiry.

## HTTP Routes

### Public

| Method | Path      | Handler      | Description  |
| ------ | --------- | ------------ | ------------ |
| GET    | `/login`  | loginPage    | Login form   |
| POST   | `/login`  | handleLogin  | Authenticate |
| POST   | `/logout` | handleLogout | End session  |

### Admin Only

| Method | Path                           | Handler          | Description                           |
| ------ | ------------------------------ | ---------------- | ------------------------------------- |
| GET    | `/admin/upload`                | uploadPage       | JSON upload form                      |
| POST   | `/admin/upload`                | handleUpload     | Import JSON file(s)                   |
| GET    | `/admin/users`                 | usersPage        | Manage teacher accounts               |
| POST   | `/admin/users`                 | handleCreateUser | Create teacher account                |
| POST   | `/admin/users/{userID}/toggle` | handleToggleUser | Activate/deactivate teacher           |
| DELETE | `/admin/exam/{examID}`         | handleDeleteExam | Delete imported exam and all its data |

### Teacher + Admin

| Method | Path                                                    | Handler           | Description                            |
| ------ | ------------------------------------------------------- | ----------------- | -------------------------------------- |
| GET    | `/`                                                     | dashboard         | List imported exams with stats         |
| GET    | `/exam/{examID}`                                        | examStudentList   | Student list (name, LLM grade, status) |
| GET    | `/exam/{examID}/student/{sessionID}`                    | reviewPage        | Per-student review page                |
| POST   | `/exam/{examID}/student/{sessionID}/score/{questionID}` | handleUpdateScore | Save teacher score + comment (htmx)    |
| POST   | `/exam/{examID}/student/{sessionID}/finalize`           | handleFinalize    | Save final grade + overall comment     |
| GET    | `/exam/{examID}/student/{sessionID}/report`             | handleReport      | Download Markdown report               |

## Key UI Pages

### Dashboard (`/`)

Lists all imported exams as cards/rows:

- Exam ID, subject, date
- Student count
- Progress: reviewed / total students

### Student List (`/exam/{examID}`)

Table of students for the selected exam:

- Student name, external ID
- LLM grade
- Review status (imported / in_review / reviewed)
- Link to review page

### Review Page (`/exam/{examID}/student/{sessionID}`)

Header:

- **Student name and external ID** prominently displayed
- Exam subject, date
- LLM grade

Per-question sections (vertically stacked):

- Question text, topic, difficulty, max points
- Expandable: rubric and model answer
- Conversation thread (student + assistant messages with timestamps)
- LLM score and feedback (read-only)
- Teacher score input (number) + comment textarea
- Save button (htmx POST, partial swap for feedback)

Footer:

- Final grade input
- Overall teacher comment textarea
- Finalize button

### Report Download

`GET /exam/{examID}/student/{sessionID}/report` returns a
`Content-Disposition: attachment` Markdown file.

## Markdown Report Format

```markdown
# Exam Report: {Subject}
**Date:** {date}
**Exam ID:** {exam_id}

**Student:** {display_name} ({external_id})
**LLM Grade:** {llm_grade}%
**Final Grade:** {final_grade}%

---

## Question 1: {topic} ({difficulty}, {max_points} pts)

> {question_text}

### Conversation

**Student** ({timestamp}):
{content}

**Examiner** ({timestamp}):
{content}

### Grading

|         | Score                        | Feedback          |
| ------- | ---------------------------- | ----------------- |
| LLM     | {llm_score}/{max_points}     | {llm_feedback}    |
| Teacher | {teacher_score}/{max_points} | {teacher_comment} |

---

## Summary

**LLM Grade:** {llm_grade}%
**Final Grade:** {final_grade}%

**Teacher's Comment:**
{teacher_comment}
```

## Import Logic

1. Parse JSON as `model.ExamExport`
2. Reject if `exam_id` already exists in DB (no implicit overwrite)
3. Insert exam metadata
4. For each `StudentResult`:
   - Insert student record
   - Insert exam session (status = `imported`, llm_grade from JSON)
   - For each `QuestionResult`:
     - Insert question with position index
     - Insert conversation messages (`ConversationMsg.At` maps to
       `conversation_messages.timestamp`)
     - Insert score row with LLM fields populated,
       `teacher_score = NULL`, `teacher_comment = ''`
5. Create `grades` row with `llm_grade` from JSON,
   `final_grade = NULL`
6. Print/return summary

### Validation

- Reject malformed JSON with clear error
- Reject duplicate exam_id
- Warn on student sessions with status other than `graded`

## Authentication

Same pattern as the examiner app:

- Password-hashed accounts with roles (admin, teacher)
- Session cookies for login state
- CSRF protection (per-session tokens)
- Admin creates teacher accounts
- Initial admin account: `GRADER_ADMIN_PASSWORD` env var or
  `--admin-password` flag creates an `admin` user on first run

## Tech Stack

- Go with chi router
- htmx 2.0 + Pico CSS v2
- SQLite via modernc.org (no CGO)
- Templ for HTML templates
- Shared `internal/model/export.go` types
