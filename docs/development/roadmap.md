# Roadmap

This document outlines the path from the current demo to a
production-ready system. Each phase builds on the previous one.

## Current state: demo

A single-user exam system that works end-to-end:

- Student answers open-ended questions in a browser
- LLM evaluates answers, asks follow-up questions, grades the exam
- Teacher reviews and overrides scores
- i18n support (English, Russian)
- Containerized deployment with Podman Quadlet + Caddy
- SQLite storage, no authentication

**What it proves:** the conversational exam flow works —
LLM-driven evaluation with follow-ups produces meaningful
assessments that teachers can refine.

## Phase 1: proof of concept (multi-user)

Goal: multiple colleagues can use the system concurrently
on a shared server.

### Authentication and user identity

- Add a `users` table (id, username, display name, role)
- Roles: `student`, `teacher`, `admin`
- Simple session-based auth (login page with username/password)
- Associate `exam_sessions` with a user ID
- Students see only their own exams; teachers see all

### Per-user data isolation

- Filter session lists by authenticated user
- Review pages visible only to teachers
- Protect routes with role-checking middleware

### Basic admin interface

- Admin page to manage users (create, disable)
- Admin page to manage question banks (upload JSON, list topics)

### Deployment

- HTTPS via Caddy (already prepared)
- SQLite remains sufficient at this scale (5-50 users)

## Phase 2: MVP

Goal: usable by a university course with real students.

### Course and exam management

- Multiple courses, each with its own question bank
- Exam blueprints configurable per course (not just CLI flags):
  number of questions, difficulty mix, time limit, follow-up count
- Teacher creates an exam from the web UI, gets a shareable link
- Exam availability windows (start/end time)

### Time limits

- Server-side enforcement of exam duration
- Client-side countdown timer (JS or htmx polling)
- Auto-submit when time expires

### Improved grading

- Structured rubric format (criteria with point weights)
- LLM grading prompt tuned per rubric criterion
- Side-by-side view: student answer, model answer, LLM assessment
- Batch re-grading: re-run LLM grading with a different model
  or updated rubric

### Question bank improvements

- Web UI for creating and editing questions (not just JSON import)
- Question tagging (multiple topics per question)
- Question difficulty auto-calibration from historical scores
- Question versioning (edit without breaking past exams)

### Reporting

- Per-student score history across exams
- Per-question analytics (average score, discrimination index)
- Exportable grade sheets (CSV)

### Database migration to PostgreSQL

- Swap `modernc.org/sqlite` for `pgx`
- Use `golang-migrate` for schema migrations
- Enables horizontal scaling and connection pooling

### Deployment improvements

- CI/CD pipeline (GitHub Actions: lint, test, build image, push)
- Helm chart or Kustomize manifests for Kubernetes/OpenShift
- Health check endpoint (`/healthz`)
- Graceful shutdown

## Phase 3: production

Goal: run reliably for multiple courses, multiple institutions.

### Multi-tenancy

- Organization/institution as a top-level entity
- Per-organization question banks, users, courses
- Organization admin role

### LLM provider management

- Support multiple LLM backends per organization
- Configurable model per exam (e.g., GPT-4o for grading,
  a smaller model for follow-up evaluation)
- Token usage tracking and cost estimation per exam
- Rate limiting and retry logic with backoff

### Security hardening

- OAuth2 / OIDC integration (e.g., Keycloak, university SSO)
- CSRF protection
- Rate limiting on public endpoints
- Audit logging (who did what, when)
- Input sanitization for Markdown in LLM responses

### Anti-cheating measures

- Randomized question order per student (already partially done
  with `--shuffle`)
- Question pool sampling: each student gets a different subset
- Session binding: prevent sharing exam URLs
- Copy-paste detection or proctoring hooks (optional)

### Scalability

- Async grading via a work queue (e.g., Redis, NATS)
  instead of synchronous in the HTTP handler
- WebSocket or SSE for real-time grading progress
- Read replicas for reporting queries
- Object storage for large attachments (diagrams, code files)

### Accessibility and UX

- Responsive design for mobile
- Keyboard navigation
- Screen reader support (ARIA attributes)
- Dark mode toggle
- Markdown rendering in student answers and LLM feedback

### API

- REST or gRPC API for programmatic access
- Webhook notifications (exam submitted, grading complete)
- LTI integration for LMS platforms (Moodle, Canvas)

## Out of scope (for now)

- Real-time collaborative exams
- Video/audio question types
- Plagiarism detection across students
- Custom LLM fine-tuning per course

## Decision log

Architectural decisions are recorded as they are made.
Each significant choice should be documented with rationale
and alternatives considered.

| Date | Decision | Rationale |
| ---- | -------- | --------- |
| 2026-02-17 | Templ over html/template | Type safety, composability, native ctx for i18n |
| 2026-02-17 | go-i18n v2 for translations | CLDR plural rules (Russian needs 4 forms), CLI tooling, JSON files |
| 2026-02-17 | pflag + Viper over stdlib flag | POSIX flags, config file support, env var unification |
| 2026-02-17 | Taskfile over Makefile | Cleaner YAML syntax, popular in Go ecosystem, cross-platform |
| 2026-02-17 | Podman Quadlet over docker-compose | Systemd integration, rootless containers, matches RHEL/OpenShift workflow |
| 2026-02-17 | Caddy over Nginx | Automatic HTTPS, simpler config, good enough for this scale |
| 2026-02-17 | SQLite for initial phases | Zero-ops, single binary, sufficient for PoC scale (50 users) |
| 2026-02-22 | Sequential int64 IDs over UUIDs | Single-instance SQLite — no distributed ID generation needed. Auth checks prevent enumeration. Readable in logs (session_id=3 vs a UUID). Revisit only if multi-instance writes or cross-database merging become a requirement |
