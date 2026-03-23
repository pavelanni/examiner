# Teacher Grading App Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone grader web app (`cmd/grader/`) that
imports exam result JSONs, lets teachers review/grade students, and
generates Markdown reports.

**Architecture:** Separate binary in the same repo, sharing
`internal/model/export.go` types. Own SQLite database, own handler
and store packages under `internal/grader/`. Follows the same
patterns as the examiner app (chi router, htmx + Pico CSS, templ
views, Cobra CLI).

**Tech Stack:** Go, chi, templ, htmx 2.0, Pico CSS v2, SQLite
(modernc.org), Cobra + Viper, bcrypt

**Spec:** `docs/superpowers/specs/2026-03-19-teacher-grading-app-design.md`

---

## File Map

### New Files

```text
cmd/grader/main.go                          -- Cobra CLI: import + serve commands
internal/grader/store/store.go              -- DB schema, constructor, migration
internal/grader/store/store_test.go         -- Store tests
internal/grader/store/user.go               -- User CRUD (same pattern as examiner)
internal/grader/store/session_auth.go       -- Auth session management
internal/grader/store/import.go             -- JSON import logic
internal/grader/store/import_test.go        -- Import tests
internal/grader/store/grading.go            -- Score update, grade finalize, queries
internal/grader/store/grading_test.go       -- Grading store tests
internal/grader/store/exam.go               -- Exam/session/student queries + delete
internal/grader/handler/handler.go          -- Handler struct, routes, middleware
internal/grader/handler/auth.go             -- Login/logout, CSRF, auth middleware
internal/grader/handler/admin.go            -- Upload, user management, delete exam
internal/grader/handler/grading.go          -- Dashboard, student list, review, finalize
internal/grader/handler/report.go           -- Markdown report download
internal/grader/handler/views/layout.templ  -- Base HTML layout
internal/grader/handler/views/login.templ   -- Login page
internal/grader/handler/views/dashboard.templ   -- Exam list dashboard
internal/grader/handler/views/students.templ    -- Student list for an exam
internal/grader/handler/views/review.templ      -- Per-student review page
internal/grader/handler/views/admin.templ       -- Upload + user management
internal/grader/report/report.go            -- Markdown report generation
internal/grader/report/report_test.go       -- Report tests
```

### Modified Files

```text
Taskfile.yml        -- Add grader build/run/test tasks
internal/model/export.go  -- No changes needed (shared as-is)
```

---

## Task 1: Store Layer — Schema and Constructor

**Files:**
- Create: `internal/grader/store/store.go`
- Create: `internal/grader/store/store_test.go`

- [ ] **Step 1: Write store constructor test**

```go
// internal/grader/store/store_test.go
package store_test

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/pavelanni/examiner/internal/grader/store"
)

func TestNew(t *testing.T) {
    dir := t.TempDir()
    dbPath := filepath.Join(dir, "test.db")

    s, err := store.New(dbPath)
    if err != nil {
        t.Fatalf("New() error: %v", err)
    }
    defer s.Close()

    if _, err := os.Stat(dbPath); err != nil {
        t.Fatalf("database file not created: %v", err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/grader/store/ -run TestNew -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Write store constructor with schema**

```go
// internal/grader/store/store.go
package store

import (
    "database/sql"
    "fmt"

    _ "modernc.org/sqlite"
)

// Store wraps the grader SQLite database.
type Store struct {
    db *sql.DB
}

// New opens (or creates) the grader database and runs migrations.
func New(dbPath string) (*Store, error) {
    db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
    if err != nil {
        return nil, fmt.Errorf("open db: %w", err)
    }
    if err := db.Ping(); err != nil {
        return nil, fmt.Errorf("ping db: %w", err)
    }
    s := &Store{db: db}
    if err := s.migrate(); err != nil {
        return nil, fmt.Errorf("migrate: %w", err)
    }
    return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
    return s.db.Close()
}

func (s *Store) migrate() error {
    _, err := s.db.Exec(`
        CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            username TEXT NOT NULL UNIQUE,
            display_name TEXT NOT NULL DEFAULT '',
            password_hash TEXT NOT NULL,
            role TEXT NOT NULL DEFAULT 'teacher',
            active INTEGER NOT NULL DEFAULT 1,
            created_at DATETIME NOT NULL
        );

        CREATE TABLE IF NOT EXISTS auth_sessions (
            id TEXT PRIMARY KEY,
            user_id INTEGER NOT NULL REFERENCES users(id),
            created_at DATETIME NOT NULL,
            expires_at DATETIME NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires
            ON auth_sessions(expires_at);

        CREATE TABLE IF NOT EXISTS exams (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            exam_id TEXT NOT NULL UNIQUE,
            subject TEXT NOT NULL DEFAULT '',
            date TEXT NOT NULL DEFAULT '',
            prompt_variant TEXT NOT NULL DEFAULT '',
            num_questions INTEGER NOT NULL DEFAULT 0,
            imported_at DATETIME NOT NULL
        );

        CREATE TABLE IF NOT EXISTS students (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            exam_db_id INTEGER NOT NULL REFERENCES exams(id) ON DELETE CASCADE,
            external_id TEXT NOT NULL DEFAULT '',
            display_name TEXT NOT NULL DEFAULT ''
        );

        CREATE TABLE IF NOT EXISTS exam_sessions (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            exam_db_id INTEGER NOT NULL REFERENCES exams(id) ON DELETE CASCADE,
            student_id INTEGER NOT NULL REFERENCES students(id) ON DELETE CASCADE,
            session_number INTEGER NOT NULL DEFAULT 1,
            status TEXT NOT NULL DEFAULT 'imported',
            started_at DATETIME,
            submitted_at DATETIME,
            llm_grade REAL NOT NULL DEFAULT 0
        );

        CREATE TABLE IF NOT EXISTS questions (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            session_id INTEGER NOT NULL REFERENCES exam_sessions(id) ON DELETE CASCADE,
            position INTEGER NOT NULL DEFAULT 0,
            text TEXT NOT NULL,
            topic TEXT NOT NULL DEFAULT '',
            difficulty TEXT NOT NULL DEFAULT '',
            max_points INTEGER NOT NULL DEFAULT 10,
            rubric TEXT NOT NULL DEFAULT '',
            model_answer TEXT NOT NULL DEFAULT ''
        );

        CREATE TABLE IF NOT EXISTS conversation_messages (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
            role TEXT NOT NULL,
            content TEXT NOT NULL,
            timestamp DATETIME
        );

        CREATE TABLE IF NOT EXISTS scores (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            question_id INTEGER NOT NULL UNIQUE REFERENCES questions(id) ON DELETE CASCADE,
            llm_score REAL NOT NULL DEFAULT 0,
            llm_feedback TEXT NOT NULL DEFAULT '',
            teacher_score REAL,
            teacher_comment TEXT NOT NULL DEFAULT ''
        );

        CREATE TABLE IF NOT EXISTS grades (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            session_id INTEGER NOT NULL UNIQUE REFERENCES exam_sessions(id) ON DELETE CASCADE,
            final_grade REAL,
            teacher_comment TEXT NOT NULL DEFAULT '',
            reviewed_by INTEGER,
            reviewed_at DATETIME
        );
    `)
    return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/grader/store/ -run TestNew -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/grader/store/store.go internal/grader/store/store_test.go
git commit -s -m "feat(grader): add store layer with schema and migration"
```

---

## Task 2: Store Layer — User and Auth Session Management

**Files:**
- Create: `internal/grader/store/user.go`
- Create: `internal/grader/store/session_auth.go`

- [ ] **Step 1: Write user store tests**

Add to `store_test.go`:

```go
func TestCreateAndGetUser(t *testing.T) {
    s := newTestStore(t)
    defer s.Close()

    id, err := s.CreateUser(model.User{
        Username:     "teacher1",
        DisplayName:  "Test Teacher",
        PasswordHash: "hash123",
        Role:         model.UserRoleTeacher,
        Active:       true,
    })
    if err != nil {
        t.Fatalf("CreateUser: %v", err)
    }
    if id == 0 {
        t.Fatal("expected non-zero ID")
    }

    u, err := s.GetUserByUsername("teacher1")
    if err != nil {
        t.Fatalf("GetUserByUsername: %v", err)
    }
    if u == nil {
        t.Fatal("expected user, got nil")
    }
    if u.DisplayName != "Test Teacher" {
        t.Errorf("DisplayName = %q, want %q", u.DisplayName, "Test Teacher")
    }
}

func TestUserCount(t *testing.T) {
    s := newTestStore(t)
    defer s.Close()

    n, err := s.UserCount()
    if err != nil {
        t.Fatalf("UserCount: %v", err)
    }
    if n != 0 {
        t.Errorf("UserCount = %d, want 0", n)
    }
}

// helper used by all tests
func newTestStore(t *testing.T) *store.Store {
    t.Helper()
    dir := t.TempDir()
    s, err := store.New(filepath.Join(dir, "test.db"))
    if err != nil {
        t.Fatalf("New: %v", err)
    }
    return s
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./internal/grader/store/ -run TestCreate -v`
Expected: FAIL — methods not defined

- [ ] **Step 3: Implement user.go**

Copy pattern from examiner's `internal/store/user.go`. Methods:
`CreateUser` (sets `created_at = time.Now()` in INSERT),
`GetUserByUsername`, `GetUserByID`, `ListUsers`,
`ToggleUserActive`, `UserCount`.

Note: add `"github.com/pavelanni/examiner/internal/model"` to the
test file imports when implementing this task.

- [ ] **Step 4: Implement session_auth.go**

Copy pattern from examiner's `internal/store/session_auth.go`.
Methods: `CreateAuthSession`, `GetAuthSession`, `DeleteAuthSession`,
`CleanupExpiredSessions`. Uses 24-hour TTL, 32-byte crypto/rand
tokens.

- [ ] **Step 5: Run tests — verify they pass**

Run: `go test ./internal/grader/store/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/grader/store/user.go internal/grader/store/session_auth.go \
        internal/grader/store/store_test.go
git commit -s -m "feat(grader): add user and auth session store"
```

---

## Task 3: Store Layer — Import Logic

**Files:**
- Create: `internal/grader/store/import.go`
- Create: `internal/grader/store/import_test.go`

- [ ] **Step 1: Write import test**

```go
// internal/grader/store/import_test.go
package store_test

import (
    "testing"
    "time"

    "github.com/pavelanni/examiner/internal/grader/store"
    "github.com/pavelanni/examiner/internal/model"
)

func sampleExport() model.ExamExport {
    now := time.Now()
    return model.ExamExport{
        ExamID:        "test-exam-1",
        Subject:       "Physics",
        Date:          "2026-03-15",
        PromptVariant: "standard",
        NumQuestions:  2,
        Results: []model.StudentResult{
            {
                ExternalID:    "STU-001",
                DisplayName:   "Alice",
                SessionNumber: 1,
                Status:        model.StatusGraded,
                StartedAt:     now,
                Questions: []model.QuestionResult{
                    {
                        Text:       "What is 2+2?",
                        Topic:      "Arithmetic",
                        Difficulty: model.DifficultyEasy,
                        MaxPoints:  10,
                        Rubric:     "Answer is 4",
                        ModelAnswer: "4",
                        Conversation: []model.ConversationMsg{
                            {Role: "student", Content: "4", At: now},
                            {Role: "assistant", Content: "Correct!", At: now},
                        },
                        LLMScore:    10,
                        LLMFeedback: "Perfect answer",
                    },
                },
                LLMGrade: 100,
            },
        },
    }
}

func TestImportExam(t *testing.T) {
    s := newTestStore(t)
    defer s.Close()

    exp := sampleExport()
    err := s.ImportExam(exp)
    if err != nil {
        t.Fatalf("ImportExam: %v", err)
    }

    // Verify exam was stored
    exams, err := s.ListExams()
    if err != nil {
        t.Fatalf("ListExams: %v", err)
    }
    if len(exams) != 1 {
        t.Fatalf("len(exams) = %d, want 1", len(exams))
    }
    if exams[0].ExamID != "test-exam-1" {
        t.Errorf("ExamID = %q, want %q", exams[0].ExamID, "test-exam-1")
    }
}

func TestImportExamDuplicate(t *testing.T) {
    s := newTestStore(t)
    defer s.Close()

    exp := sampleExport()
    if err := s.ImportExam(exp); err != nil {
        t.Fatalf("first import: %v", err)
    }
    if err := s.ImportExam(exp); err == nil {
        t.Fatal("expected error on duplicate import, got nil")
    }
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./internal/grader/store/ -run TestImport -v`
Expected: FAIL — ImportExam not defined

- [ ] **Step 3: Implement import.go**

```go
// internal/grader/store/import.go
package store

import (
    "fmt"
    "time"

    "github.com/pavelanni/examiner/internal/model"
)

// ExamSummary holds metadata for display in the dashboard.
type ExamSummary struct {
    ID            int64
    ExamID        string
    Subject       string
    Date          string
    NumQuestions  int
    StudentCount  int
    ReviewedCount int
    ImportedAt    time.Time
}

// ImportExam inserts all data from an ExamExport into the database.
// Returns an error if the exam_id already exists.
func (s *Store) ImportExam(exp model.ExamExport) error {
    tx, err := s.db.Begin()
    if err != nil {
        return fmt.Errorf("begin tx: %w", err)
    }
    defer tx.Rollback()

    // Check for duplicate
    var count int
    err = tx.QueryRow("SELECT COUNT(*) FROM exams WHERE exam_id = ?",
        exp.ExamID).Scan(&count)
    if err != nil {
        return fmt.Errorf("check duplicate: %w", err)
    }
    if count > 0 {
        return fmt.Errorf("exam %q already imported", exp.ExamID)
    }

    // Insert exam
    res, err := tx.Exec(`INSERT INTO exams
        (exam_id, subject, date, prompt_variant, num_questions, imported_at)
        VALUES (?, ?, ?, ?, ?, ?)`,
        exp.ExamID, exp.Subject, exp.Date, exp.PromptVariant,
        exp.NumQuestions, time.Now())
    if err != nil {
        return fmt.Errorf("insert exam: %w", err)
    }
    examDBID, _ := res.LastInsertId()

    for _, sr := range exp.Results {
        // Warn on non-graded sessions
        if sr.Status != model.StatusGraded {
            slog.Warn("importing session with non-graded status",
                "student", sr.ExternalID, "status", sr.Status)
        }

        // Insert student
        res, err = tx.Exec(`INSERT INTO students
            (exam_db_id, external_id, display_name) VALUES (?, ?, ?)`,
            examDBID, sr.ExternalID, sr.DisplayName)
        if err != nil {
            return fmt.Errorf("insert student %s: %w", sr.ExternalID, err)
        }
        studentID, _ := res.LastInsertId()

        // Insert exam session
        res, err = tx.Exec(`INSERT INTO exam_sessions
            (exam_db_id, student_id, session_number, status,
             started_at, submitted_at, llm_grade)
            VALUES (?, ?, ?, 'imported', ?, ?, ?)`,
            examDBID, studentID, sr.SessionNumber,
            sr.StartedAt, sr.SubmittedAt, sr.LLMGrade)
        if err != nil {
            return fmt.Errorf("insert session: %w", err)
        }
        sessionID, _ := res.LastInsertId()

        for pos, qr := range sr.Questions {
            // Insert question
            res, err = tx.Exec(`INSERT INTO questions
                (session_id, position, text, topic, difficulty,
                 max_points, rubric, model_answer)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
                sessionID, pos, qr.Text, qr.Topic, qr.Difficulty,
                qr.MaxPoints, qr.Rubric, qr.ModelAnswer)
            if err != nil {
                return fmt.Errorf("insert question: %w", err)
            }
            questionID, _ := res.LastInsertId()

            // Insert conversation messages
            for _, msg := range qr.Conversation {
                _, err = tx.Exec(`INSERT INTO conversation_messages
                    (question_id, role, content, timestamp)
                    VALUES (?, ?, ?, ?)`,
                    questionID, msg.Role, msg.Content, msg.At)
                if err != nil {
                    return fmt.Errorf("insert message: %w", err)
                }
            }

            // Insert score (LLM fields only, teacher fields NULL)
            _, err = tx.Exec(`INSERT INTO scores
                (question_id, llm_score, llm_feedback)
                VALUES (?, ?, ?)`,
                questionID, qr.LLMScore, qr.LLMFeedback)
            if err != nil {
                return fmt.Errorf("insert score: %w", err)
            }
        }

        // Insert grade row (final_grade NULL)
        _, err = tx.Exec(`INSERT INTO grades (session_id) VALUES (?)`,
            sessionID)
        if err != nil {
            return fmt.Errorf("insert grade: %w", err)
        }
    }

    return tx.Commit()
}

// ListExams returns all imported exams with summary stats.
func (s *Store) ListExams() ([]ExamSummary, error) {
    rows, err := s.db.Query(`
        SELECT e.id, e.exam_id, e.subject, e.date, e.num_questions,
            e.imported_at,
            COUNT(DISTINCT es.id) AS student_count,
            COUNT(DISTINCT CASE WHEN es.status = 'reviewed'
                THEN es.id END) AS reviewed_count
        FROM exams e
        LEFT JOIN exam_sessions es ON es.exam_db_id = e.id
        GROUP BY e.id
        ORDER BY e.imported_at DESC`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var exams []ExamSummary
    for rows.Next() {
        var ex ExamSummary
        if err := rows.Scan(&ex.ID, &ex.ExamID, &ex.Subject, &ex.Date,
            &ex.NumQuestions, &ex.ImportedAt,
            &ex.StudentCount, &ex.ReviewedCount); err != nil {
            return nil, err
        }
        exams = append(exams, ex)
    }
    return exams, rows.Err()
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./internal/grader/store/ -run TestImport -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/grader/store/import.go internal/grader/store/import_test.go
git commit -s -m "feat(grader): add JSON import logic and exam listing"
```

---

## Task 4: Store Layer — Exam Queries and Delete

**Files:**
- Create: `internal/grader/store/exam.go`
- Modify: `internal/grader/store/store_test.go`

- [ ] **Step 1: Write tests for exam queries**

```go
func TestListStudentsForExam(t *testing.T) {
    s := newTestStore(t)
    defer s.Close()
    s.ImportExam(sampleExport())

    students, err := s.ListStudentsForExam("test-exam-1")
    if err != nil {
        t.Fatalf("ListStudentsForExam: %v", err)
    }
    if len(students) != 1 {
        t.Fatalf("len = %d, want 1", len(students))
    }
    if students[0].DisplayName != "Alice" {
        t.Errorf("name = %q, want Alice", students[0].DisplayName)
    }
}

func TestDeleteExam(t *testing.T) {
    s := newTestStore(t)
    defer s.Close()
    s.ImportExam(sampleExport())

    err := s.DeleteExam("test-exam-1")
    if err != nil {
        t.Fatalf("DeleteExam: %v", err)
    }
    exams, _ := s.ListExams()
    if len(exams) != 0 {
        t.Fatalf("len(exams) = %d after delete, want 0", len(exams))
    }
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./internal/grader/store/ -run "TestListStudents|TestDelete" -v`
Expected: FAIL

- [ ] **Step 3: Implement exam.go**

Types and methods: `StudentListItem` struct (session ID, external ID,
display name, LLM grade, status), `ListStudentsForExam(examID string)`,
`DeleteExam(examID string)`, `GetExamByID(examID string)`.

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./internal/grader/store/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/grader/store/exam.go internal/grader/store/store_test.go
git commit -s -m "feat(grader): add exam queries and delete"
```

---

## Task 5: Store Layer — Grading Operations

**Files:**
- Create: `internal/grader/store/grading.go`
- Create: `internal/grader/store/grading_test.go`

- [ ] **Step 1: Write grading tests**

Tests for:
- `GetReviewData(sessionID)` — returns full session with questions,
  conversations, scores for the review page
- `UpdateTeacherScore(questionID, score, comment)` — updates teacher
  fields in scores table
- `FinalizeGrade(sessionID, finalGrade, teacherComment, reviewerID)`
  — sets grade and marks session as reviewed
- Verify session status transitions: imported → in_review → reviewed

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./internal/grader/store/ -run TestGrad -v`
Expected: FAIL

- [ ] **Step 3: Implement grading.go**

Define `ReviewData` struct:

```go
type ReviewData struct {
    ExamID      string
    Subject     string
    Date        string
    SessionID   int64
    StudentName string
    ExternalID  string
    LLMGrade    float64
    Status      string
    Questions   []ReviewQuestion
    Grade       *GradeInfo
}

type ReviewQuestion struct {
    ID           int64
    Position     int
    Text         string
    Topic        string
    Difficulty   string
    MaxPoints    int
    Rubric       string
    ModelAnswer  string
    Messages     []ConversationMessage
    LLMScore     float64
    LLMFeedback  string
    TeacherScore *float64
    TeacherComment string
}

type ConversationMessage struct {
    Role      string
    Content   string
    Timestamp time.Time
}

type GradeInfo struct {
    FinalGrade     *float64
    TeacherComment string
    ReviewedBy     *int64
    ReviewedAt     *time.Time
}
```

Methods: `GetReviewData(sessionID int64)`,
`UpdateTeacherScore(questionID int64, score float64, comment string)`,
`FinalizeGrade(sessionID int64, finalGrade float64, comment string, reviewerID int64)`,
`SetSessionStatus(sessionID int64, status string)`.

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./internal/grader/store/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/grader/store/grading.go internal/grader/store/grading_test.go
git commit -s -m "feat(grader): add grading operations store"
```

---

## Task 6: Markdown Report Generation

**Files:**
- Create: `internal/grader/report/report.go`
- Create: `internal/grader/report/report_test.go`

- [ ] **Step 1: Write report test**

```go
// internal/grader/report/report_test.go
package report_test

import (
    "strings"
    "testing"

    "github.com/pavelanni/examiner/internal/grader/report"
    "github.com/pavelanni/examiner/internal/grader/store"
)

func TestGenerate(t *testing.T) {
    score := 8.0
    grade := 85.0
    data := store.ReviewData{
        Subject:     "Physics",
        ExamID:      "phys-2026",
        Date:        "2026-03-15",
        StudentName: "Alice Johnson",
        ExternalID:  "STU-001",
        LLMGrade:    92.7,
        Questions: []store.ReviewQuestion{
            {
                Position:    0,
                Text:        "What is gravity?",
                Topic:       "Mechanics",
                Difficulty:  "easy",
                MaxPoints:   10,
                LLMScore:    9,
                LLMFeedback: "Good answer",
                TeacherScore: &score,
                TeacherComment: "Needs more detail",
                Messages: []store.ConversationMessage{
                    {Role: "student", Content: "Force of attraction"},
                    {Role: "assistant", Content: "Correct, elaborate?"},
                },
            },
        },
        Grade: &store.GradeInfo{
            FinalGrade:     &grade,
            TeacherComment: "Good overall",
        },
    }

    md := report.Generate(data)

    checks := []string{
        "# Exam Report: Physics",
        "**Student:** Alice Johnson (STU-001)",
        "**LLM Grade:** 92.7%",
        "**Final Grade:** 85.0%",
        "Question 1: Mechanics",
        "Force of attraction",
        "| LLM | 9.0/10 |",
        "| Teacher | 8.0/10 |",
        "Good overall",
    }
    for _, c := range checks {
        if !strings.Contains(md, c) {
            t.Errorf("report missing %q", c)
        }
    }
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `go test ./internal/grader/report/ -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement report.go**

```go
// internal/grader/report/report.go
package report

import (
    "fmt"
    "strings"

    "github.com/pavelanni/examiner/internal/grader/store"
)

// Generate produces a Markdown report for a student's graded session.
func Generate(data store.ReviewData) string {
    var b strings.Builder

    fmt.Fprintf(&b, "# Exam Report: %s\n\n", data.Subject)
    fmt.Fprintf(&b, "**Date:** %s\n", data.Date)
    fmt.Fprintf(&b, "**Exam ID:** %s\n\n", data.ExamID)
    fmt.Fprintf(&b, "**Student:** %s (%s)\n", data.StudentName, data.ExternalID)
    fmt.Fprintf(&b, "**LLM Grade:** %.1f%%\n", data.LLMGrade)

    if data.Grade != nil && data.Grade.FinalGrade != nil {
        fmt.Fprintf(&b, "**Final Grade:** %.1f%%\n", *data.Grade.FinalGrade)
    }

    b.WriteString("\n---\n\n")

    for i, q := range data.Questions {
        fmt.Fprintf(&b, "## Question %d: %s (%s, %d pts)\n\n",
            i+1, q.Topic, q.Difficulty, q.MaxPoints)
        fmt.Fprintf(&b, "> %s\n\n", q.Text)

        if len(q.Messages) > 0 {
            b.WriteString("### Conversation\n\n")
            for _, m := range q.Messages {
                role := "Student"
                if m.Role == "assistant" {
                    role = "Examiner"
                }
                if !m.Timestamp.IsZero() {
                    fmt.Fprintf(&b, "**%s** (%s):\n",
                        role, m.Timestamp.Format("15:04"))
                } else {
                    fmt.Fprintf(&b, "**%s**:\n", role)
                }
                fmt.Fprintf(&b, "%s\n\n", m.Content)
            }
        }

        b.WriteString("### Grading\n\n")
        b.WriteString("| | Score | Feedback |\n")
        b.WriteString("|---|---|---|\n")
        fmt.Fprintf(&b, "| LLM | %.1f/%d | %s |\n",
            q.LLMScore, q.MaxPoints, q.LLMFeedback)
        if q.TeacherScore != nil {
            fmt.Fprintf(&b, "| Teacher | %.1f/%d | %s |\n",
                *q.TeacherScore, q.MaxPoints, q.TeacherComment)
        }

        b.WriteString("\n---\n\n")
    }

    b.WriteString("## Summary\n\n")
    fmt.Fprintf(&b, "**LLM Grade:** %.1f%%\n", data.LLMGrade)
    if data.Grade != nil && data.Grade.FinalGrade != nil {
        fmt.Fprintf(&b, "**Final Grade:** %.1f%%\n", *data.Grade.FinalGrade)
    }
    if data.Grade != nil && data.Grade.TeacherComment != "" {
        fmt.Fprintf(&b, "\n**Teacher's Comment:**\n%s\n",
            data.Grade.TeacherComment)
    }

    return b.String()
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `go test ./internal/grader/report/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/grader/report/report.go internal/grader/report/report_test.go
git commit -s -m "feat(grader): add Markdown report generation"
```

---

## Task 7: Templ Views

**Files:**
- Create: `internal/grader/handler/views/layout.templ`
- Create: `internal/grader/handler/views/login.templ`
- Create: `internal/grader/handler/views/dashboard.templ`
- Create: `internal/grader/handler/views/students.templ`
- Create: `internal/grader/handler/views/review.templ`
- Create: `internal/grader/handler/views/admin.templ`

- [ ] **Step 1: Create layout.templ**

Base HTML layout with Pico CSS and htmx includes. Navigation bar
shows "Grader" title, username, and logout link. Admin users see
"Upload" and "Users" nav links. Helper functions: `csrf(ctx)`,
`messageClass(role)`.

- [ ] **Step 2: Create login.templ**

Login form with username/password fields and CSRF token. Shows
error message if login fails.

- [ ] **Step 3: Create dashboard.templ**

Table of imported exams showing: exam ID (as link), subject, date,
students (reviewed/total), imported at.

- [ ] **Step 4: Create students.templ**

Table of students for an exam showing: student name, external ID,
LLM grade, status badge, link to review page.

- [ ] **Step 5: Create review.templ**

The core grading page. Header with **student name and external ID
prominently displayed**, exam info, LLM grade. Per-question sections
with conversation, rubric/model answer in `<details>`, LLM score
(read-only), teacher score input + comment textarea + save button
(htmx POST). Footer with final grade input, overall comment, and
finalize button.

- [ ] **Step 6: Create admin.templ**

Combined admin page with:
- File upload form (multipart, accepts .json)
- User list table with toggle active button
- Create user form (username, display name, password, role select)

- [ ] **Step 7: Run templ generate**

Run: `templ generate`
Expected: generates `*_templ.go` files without errors

- [ ] **Step 8: Commit**

```bash
git add internal/grader/handler/views/
git commit -s -m "feat(grader): add templ views for all pages"
```

---

## Task 8: HTTP Handler — Auth and Middleware

**Files:**
- Create: `internal/grader/handler/handler.go`
- Create: `internal/grader/handler/auth.go`

- [ ] **Step 1: Create handler.go**

```go
// internal/grader/handler/handler.go
package handler

import (
    "net/http"

    "github.com/go-chi/chi/v5"
    "github.com/go-chi/chi/v5/middleware"
    "github.com/pavelanni/examiner/internal/grader/store"
    "github.com/pavelanni/examiner/internal/model"
)

// Handler holds dependencies for HTTP request handling.
type Handler struct {
    store         *store.Store
    secureCookies bool
}

// New creates a Handler.
func New(s *store.Store, secureCookies bool) *Handler {
    return &Handler{store: s, secureCookies: secureCookies}
}

// Routes returns the chi router with all routes configured.
func (h *Handler) Routes() http.Handler {
    r := chi.NewRouter()
    r.Use(middleware.Logger)
    r.Use(middleware.Recoverer)

    // Public
    r.Group(func(r chi.Router) {
        r.Use(h.csrfMiddleware)
        r.Get("/login", h.loginPage)
        r.Post("/login", h.handleLogin)
    })

    // Protected
    r.Group(func(r chi.Router) {
        r.Use(h.requireAuth)
        r.Use(h.csrfMiddleware)
        r.Post("/logout", h.handleLogout)

        // Teacher + Admin
        r.Get("/", h.dashboard)
        r.Get("/exam/{examID}", h.examStudentList)
        r.Get("/exam/{examID}/student/{sessionID}", h.reviewPage)
        r.Post("/exam/{examID}/student/{sessionID}/score/{questionID}",
            h.handleUpdateScore)
        r.Post("/exam/{examID}/student/{sessionID}/finalize",
            h.handleFinalize)
        r.Get("/exam/{examID}/student/{sessionID}/report",
            h.handleReport)

        // Admin only
        r.Group(func(r chi.Router) {
            r.Use(requireRole(model.UserRoleAdmin))
            r.Get("/admin/upload", h.uploadPage)
            r.Post("/admin/upload", h.handleUpload)
            r.Get("/admin/users", h.usersPage)
            r.Post("/admin/users", h.handleCreateUser)
            r.Post("/admin/users/{userID}/toggle", h.handleToggleUser)
            r.Delete("/admin/exam/{examID}", h.handleDeleteExam)
        })
    })

    return r
}
```

- [ ] **Step 2: Create auth.go**

Implement `loginPage`, `handleLogin`, `handleLogout`,
`csrfMiddleware`, `requireAuth`, `requireRole`. Follow examiner
patterns exactly:
- Session cookie: `session`, HttpOnly, SameSite=Lax
- CSRF: per-session (no rotation), constant-time comparison
- Login redirects to `/`
- `requireAuth` injects user into context via `model.ContextWithUser`
- `requireRole` uses variadic `allowed ...model.UserRole` parameter
  (same signature as examiner)

- [ ] **Step 3: Verify compilation**

Run: `go build ./internal/grader/handler/`
Expected: may fail due to missing handler methods — that's OK, we
add stubs in next tasks

- [ ] **Step 4: Commit**

```bash
git add internal/grader/handler/handler.go internal/grader/handler/auth.go
git commit -s -m "feat(grader): add handler with auth and routing"
```

---

## Task 9: HTTP Handler — Admin Routes

**Files:**
- Create: `internal/grader/handler/admin.go`

- [ ] **Step 1: Implement admin handlers**

Methods:
- `uploadPage` — renders upload form
- `handleUpload` — parses multipart form, reads JSON file(s),
  calls `store.ImportExam`, shows success/error
- `usersPage` — lists users, renders create form
- `handleCreateUser` — parses form, bcrypt hash, calls
  `store.CreateUser`
- `handleToggleUser` — calls `store.ToggleUserActive`
- `handleDeleteExam` — calls `store.DeleteExam`, redirects to `/`

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/grader/handler/`

- [ ] **Step 3: Commit**

```bash
git add internal/grader/handler/admin.go
git commit -s -m "feat(grader): add admin handlers (upload, users, delete)"
```

---

## Task 10: HTTP Handler — Grading Routes

**Files:**
- Create: `internal/grader/handler/grading.go`

- [ ] **Step 1: Implement grading handlers**

Methods:
- `dashboard` — calls `store.ListExams`, renders dashboard
- `examStudentList` — calls `store.ListStudentsForExam`, renders
  student table
- `reviewPage` — calls `store.GetReviewData`, renders review page
  with student name prominently displayed
- `handleUpdateScore` — parses form (teacher_score, teacher_comment),
  validates score ≤ max_points, calls `store.UpdateTeacherScore`,
  sets session to `in_review` if still `imported`. Returns htmx
  partial with success indicator.
- `handleFinalize` — parses final_grade (0-100) + teacher_comment,
  calls `store.FinalizeGrade`, redirects to student list

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/grader/handler/`

- [ ] **Step 3: Commit**

```bash
git add internal/grader/handler/grading.go
git commit -s -m "feat(grader): add grading handlers (dashboard, review, finalize)"
```

---

## Task 11: HTTP Handler — Report Download

**Files:**
- Create: `internal/grader/handler/report.go`

- [ ] **Step 1: Implement report handler**

```go
func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
    sessionID, _ := strconv.ParseInt(chi.URLParam(r, "sessionID"), 10, 64)
    data, err := h.store.GetReviewData(sessionID)
    if err != nil {
        http.Error(w, "session not found", http.StatusNotFound)
        return
    }
    md := report.Generate(*data)
    filename := fmt.Sprintf("%s-%s-report.md", data.ExamID, data.ExternalID)
    w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
    w.Header().Set("Content-Disposition",
        fmt.Sprintf("attachment; filename=%q", filename))
    w.Write([]byte(md))
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/grader/handler/`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/grader/handler/report.go
git commit -s -m "feat(grader): add Markdown report download handler"
```

---

## Task 12: CLI Entry Point

**Files:**
- Create: `cmd/grader/main.go`

- [ ] **Step 1: Implement main.go with Cobra commands**

Two commands:
- `grader serve` — starts HTTP server
  - Flags: `--port` (8082), `--db` (grader.db),
    `--admin-password` / `GRADER_ADMIN_PASSWORD`,
    `--secure-cookies` (default true)
  - On startup: open store, seed admin if no users exist, start server
- `grader import` — imports JSON files
  - Args: file paths (1 or more)
  - Flag: `--db` (grader.db)
  - For each file: read, parse as `model.ExamExport`, call
    `store.ImportExam`, print summary

Use Viper with `GRADER_` env prefix. Log with `log/slog`.

- [ ] **Step 2: Build and verify**

Run: `go build -o grader ./cmd/grader/`
Expected: binary builds successfully

- [ ] **Step 3: Smoke test import**

Run:
```bash
./grader import --db /tmp/test-grader.db \
    examples/exam-2026-03-07/phys-2026-spring-g1-results.json
```
Expected: prints summary with exam ID and student count

- [ ] **Step 4: Smoke test serve**

Run:
```bash
GRADER_ADMIN_PASSWORD=test123 ./grader serve \
    --db /tmp/test-grader.db --port 8082 --secure-cookies=false
```
Expected: server starts, login page accessible at
`http://localhost:8082/login`

- [ ] **Step 5: Commit**

```bash
git add cmd/grader/main.go
git commit -s -m "feat(grader): add CLI entry point with import and serve commands"
```

---

## Task 13: Taskfile Updates

**Files:**
- Modify: `Taskfile.yml`

- [ ] **Step 1: Add grader tasks to Taskfile.yml**

Add these tasks:

```yaml
grader-build:
  desc: Build the grader binary
  deps: [generate]
  cmds:
    - go build -o grader ./cmd/grader/

grader-run:
  desc: Build and run the grader server
  deps: [grader-build]
  cmds:
    - ./grader serve {{.CLI_ARGS}}

grader-import:
  desc: Import exam results into grader
  deps: [grader-build]
  cmds:
    - ./grader import {{.CLI_ARGS}}
```

- [ ] **Step 2: Verify tasks work**

Run: `task grader-build`
Expected: builds `grader` binary

- [ ] **Step 3: Commit**

```bash
git add Taskfile.yml
git commit -s -m "feat(grader): add grader build/run tasks to Taskfile"
```

---

## Task 14: Integration Test — Full Workflow

**Files:**
- Modify: `internal/grader/store/store_test.go`

- [ ] **Step 1: Write end-to-end store test**

Test the full workflow:
1. Import sample exam
2. List exams — verify count
3. List students — verify names
4. Get review data — verify questions and conversations
5. Update teacher score
6. Finalize grade
7. Verify session status = reviewed
8. Generate report — verify content

- [ ] **Step 2: Run full test suite**

Run: `go test ./internal/grader/... -v`
Expected: all PASS

- [ ] **Step 3: Run linter**

Run: `task lint`
Expected: no errors (or fix any that appear)

- [ ] **Step 4: Commit**

```bash
git add internal/grader/store/store_test.go
git commit -s -m "test(grader): add integration test for full grading workflow"
```

---

## Task 15: Manual Testing and Polish

- [ ] **Step 1: Import example data and start server**

```bash
task grader-build
./grader import --db /tmp/grader-test.db \
    examples/exam-2026-03-07/phys-2026-spring-g1-results.json
GRADER_ADMIN_PASSWORD=admin123 ./grader serve \
    --db /tmp/grader-test.db --port 8082 --secure-cookies=false
```

- [ ] **Step 2: Test login flow**

Open `http://localhost:8082/login`, log in as admin/admin123.

- [ ] **Step 3: Test dashboard and navigation**

Verify dashboard shows the imported exam with correct stats.
Click through to student list, verify names and LLM grades.

- [ ] **Step 4: Test review page**

Open a student's review page. Verify:
- Student name and ID are prominently displayed
- All questions show with conversations
- LLM scores and feedback are visible
- Rubric and model answer are in expandable sections
- Teacher score and comment fields work

- [ ] **Step 5: Test grading workflow**

- Save teacher scores for each question
- Enter final grade and comment
- Click finalize
- Verify session status changes to "reviewed"

- [ ] **Step 6: Test report download**

Download Markdown report, verify it contains all expected sections.

- [ ] **Step 7: Test admin features**

- Upload a JSON file via the web UI
- Create a teacher account
- Toggle teacher active status
- Delete an exam

- [ ] **Step 8: Fix any issues found**

Address UI/UX issues, validation gaps, or bugs discovered.

- [ ] **Step 9: Final commit**

```bash
git add -A
git commit -s -m "fix(grader): polish from manual testing"
```

- [ ] **Step 10: Run full verification**

```bash
task lint
go test ./internal/grader/... -v
```
