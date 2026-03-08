# Multi-session exam deployment

This document describes the plan for running multiple concurrent
exam sessions, each with its own container, and consolidating
results into a separate grading application.

## Context

Currently the examiner runs as a single instance (or two for
en/ru). Each instance has one set of questions, one grading
strictness level, and one shared database. This works for a
single exam group but not for an exam day with multiple groups
taking different subjects.

**Tagged baseline:** `v0.1.0` — single-instance examiner with
LLM grading and teacher review.

## Goal

Run 10+ exam groups concurrently on the same server. Each group
has its own subject, question bank, grading level, and student
roster. After exams finish, export results and destroy containers.

Teacher review happens in a **separate grading application**
that imports exported results from all exam instances.

## Architecture split

The examiner app becomes a focused exam-taking + auto-grading
tool. Teacher review moves to a separate grading app.

| Concern | Examiner | Grading app |
| ------- | -------- | ----------- |
| Users | Students + admin | Teachers + admin |
| Core action | Answer questions, LLM grading | Review answers, override scores |
| Lifecycle | Ephemeral (per exam) | Persistent |
| Data | Single group's questions + roster | Consolidated results |
| Deployment | Many instances, short-lived | One instance, long-lived |

## Teacher workflow

A teacher preparing an exam provides the following by the
deadline (e.g. Tuesday for a Thursday exam):

1. Student roster (CSV with university IDs and names)
1. Questions with model answers (JSON); if answers are missing,
   LLM generates them for teacher review before exam day
1. Number of questions per session
1. Time limit per session (future feature, tracked in Issues)
1. Grading strictness: `strict`, `standard`, or `lenient`

## Exam manifest

A single YAML file describes one exam group:

```yaml
exam_id: physics-g1
subject: Physics
date: 2026-03-05
lang: en
prompt_variant: strict
num_questions: 5
max_followups: 3
shuffle: true
questions: questions/physics_en.json
roster: rosters/physics-g1.csv
```

The `examiner prep` subcommand reads this manifest and produces
a ready-to-run exam package: seeded SQLite database, generated
credentials file, and container configuration.

## Export format (contract between apps)

Each exam instance exports a self-contained JSON file. No internal
integer IDs leak out. The unique key for a student result is
`(exam_id, date, external_id)`.

```json
{
  "exam_id": "physics-g1",
  "subject": "Physics",
  "date": "2026-03-05",
  "prompt_variant": "strict",
  "num_questions": 5,
  "results": [
    {
      "external_id": "UNI-12345",
      "display_name": "Ivanov Alexei",
      "session_number": 1,
      "status": "graded",
      "started_at": "2026-03-05T09:00:00Z",
      "submitted_at": "2026-03-05T09:45:00Z",
      "questions": [
        {
          "text": "Explain Newton's third law...",
          "topic": "mechanics",
          "difficulty": "medium",
          "max_points": 10,
          "rubric": "...",
          "model_answer": "...",
          "conversation": [
            {"role": "student", "content": "...", "at": "..."},
            {"role": "assistant", "content": "...", "at": "..."}
          ],
          "llm_score": 8.5,
          "llm_feedback": "Good explanation but..."
        }
      ],
      "llm_grade": 7.8
    }
  ]
}
```

## Student onboarding

1. Teacher provides a roster CSV:

   ```csv
   student_id,display_name
   UNI-12345,Ivanov Alexei
   UNI-67890,Petrova Maria
   ```

1. `examiner prep` processes the roster:
   - Creates the SQLite database and seeds the admin user
   - For each student: generates a random password, hashes it,
     inserts the user with the university ID as `external_id`
   - Outputs a credentials file for distribution to students

1. Passwords are ephemeral (container dies after the exam) so
   they only need to be unique, not strong. Format example:
   `phys-a3x7k` (subject prefix + random suffix).

1. LDAP integration is out of scope for now.

## Schema changes in examiner

- Add `external_id TEXT` column to `users` table (nullable,
  stores university ID)
- Remove teacher review routes and templates (moved to
  grading app)
- Remove `teacher` and `admin` user roles; all runtime users
  are students (see "Admin role" below)
- Remove admin UI (user management, question upload) — these
  functions move to the `examiner prep` CLI tool
- Keep `teacher_score` / `final_grade` columns in schema for
  now (they are populated only by export/import, not by the
  examiner UI)

### Admin role

In the pre-seeded container model, the admin UI becomes
unnecessary: `examiner prep` handles user creation and question
loading before the container starts. Keeping an admin login
in the running exam container increases the attack surface
without adding value.

If a student was left off the roster, the fix is to stop the
container, re-run `prep` with the updated roster, and restart.
This takes seconds and avoids exposing a privileged endpoint
during the exam.

## Deployment workflow

Containers are started one-by-one. The deploy script finds an
available port for each instance.

```text
examiner prep --manifest thursday-exams.yaml
  → produces exam packages in thursday-exams/

deploy-exams.sh thursday-exams/
  → starts containers, finds ports, updates Caddy

  ... exam day ...

collect-results.sh thursday-exams/
  → runs `examiner export` on each instance, consolidates

teardown-exams.sh thursday-exams/
  → stops containers, removes volumes
```

Wildcard DNS (`*.examiner.pavelanni.dev`) set once in
Cloudflare eliminates per-exam DNS changes. Each group gets
a subdomain like `physics-g1.examiner.pavelanni.dev`.

## Implementation steps

These are ordered by dependency. Steps that do not change
existing behavior are listed first.

1. **Add `external_id` to users table** — small schema
   migration, needed for meaningful exports

1. **`examiner export` subcommand** — reads the database,
   outputs the JSON format described above; the critical
   bridge to the grading app

1. **`examiner prep` subcommand** — reads manifest YAML +
   roster CSV, produces seeded database + credentials file

1. **Deploy / teardown scripts** — Taskfile targets or shell
   scripts wrapping container lifecycle and Caddy config

1. **Strip teacher review from examiner** — remove routes,
   templates, and teacher role; examiner becomes student-only

1. **Grading app** — separate project; imports JSON exports,
   provides teacher review UI across all exam groups

Steps 1--4 are additive and preserve backward compatibility
with `v0.1.0`. Step 5 is a cleanup. Step 6 is a new project.

## Future: Kubernetes migration

The container-per-group model maps directly to Kubernetes
primitives:

| Current setup | Kubernetes equivalent |
| ------------- | --------------------- |
| Exam manifest YAML | Kustomize overlay |
| Podman container | Deployment (1 replica) |
| Auto-assigned port | Service (ClusterIP) |
| Caddy subdomain routing | Ingress / Gateway API HTTPRoute |
| Seeded SQLite volume | PVC + init container |
| `teardown-exams.sh` | `kubectl delete -k overlays/physics-g1/` |

A Kustomize base layer would define a generic exam Deployment +
Service + HTTPRoute. Each exam group becomes an overlay that
patches the image config, mounts its own questions and roster,
and sets environment variables for grading strictness and
question count.

**Why not now:** a single VPS with Podman handles 10 containers
comfortably. Kubernetes adds operational overhead (cluster,
storage provisioner, image pull secrets) that is not justified
at this scale.

**When to revisit:** multiple exam days overlapping, multiple
servers needed, or if the project moves to a university
infrastructure team that already runs OpenShift/Kubernetes.

**Migration path:** the exam manifest, `prep`/`export`
subcommands, and JSON export format all transfer unchanged.
The migration is a deployment concern, not an app rewrite.

## Future: self-assessment mode

The examiner can serve as a self-assessment and learning tool
during the semester, not just for graded exams. Several teachers
have expressed interest in this use case.

**How it differs from exam mode:**

- **Always available** — a persistent instance, not ephemeral
- **No roster restriction** — any student can sign up or use
  it anonymously
- **Higher follow-up count** — increase `max-followups` to 5+
  so the LLM can guide the student through a topic in a
  Socratic dialogue style
- **No export to grading** — results are for the student only;
  no teacher review needed
- **Lenient grading** — feedback-oriented, not score-oriented

**What it reuses:** the same exam engine, question banks, and
LLM integration. The only differences are configuration
parameters (`max-followups`, `prompt-variant`) and the absence
of the export/grading pipeline.

**Open questions:**

- Should self-assessment sessions be persistent (student can
  return and review past attempts) or ephemeral?
- Should teachers see aggregate analytics (e.g. "70% of
  students struggle with thermodynamics") without seeing
  individual sessions?
- Does this need its own instance or can it coexist with exam
  instances on the same server?

## Decisions

| Date | Decision | Rationale |
| ---- | -------- | --------- |
| 2026-02-28 | Container-per-group, not multi-tenant | Zero code changes for isolation; matches ephemeral exam lifecycle; multi-tenant would require major refactoring |
| 2026-02-28 | Export to JSON, not UUID-based DB merge | Additive (new subcommand), no schema migration; composite key `(exam_id, date, external_id)` avoids ID collisions without UUIDs |
| 2026-02-28 | Split teacher review into separate grading app | Examiner is ephemeral; teacher needs persistent cross-group view; avoids reviewing in N separate containers |
| 2026-02-28 | Sequential port assignment by deploy script | Simple; no orchestrator needed; script checks availability at deploy time |
| 2026-02-28 | Wildcard DNS in Cloudflare | One-time setup; unlimited subdomains without per-exam DNS changes |
