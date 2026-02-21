# Configuration flexibility improvements

Ideas for making examiner configuration more flexible.
Organized by area, roughly in priority order.

## Question management

- **Reload questions without deleting the DB** — a `--reload-questions`
  flag or admin endpoint that re-imports from JSON, updating existing
  questions and adding new ones (matching by text or an explicit `id` field)
- **Multiple question banks** — support loading from multiple JSON files
  or a directory (`--questions ./questions.d/`), with each file tagged
  by its filename or an explicit `bank` field
- **Question bank selection at exam time** — instead of one global set,
  let the user pick which bank to use when starting an exam
  (dropdown on the index page)

## Exam profiles

Instead of global flags like `--num-questions 3 --difficulty medium --shuffle`,
define named exam profiles in the config file:

```yaml
profiles:
  quick-review:
    num-questions: 5
    difficulty: easy
    max-followups: 1
    shuffle: true
  full-exam:
    num-questions: 20
    difficulty: medium,hard
    max-followups: 3
    shuffle: true
    time-limit: 45m
```

The index page would show a profile selector. This avoids restarting
the server to change exam parameters.

## LLM configuration

- **Per-task model selection** — use a cheaper/faster model for follow-up
  evaluation and a stronger one for final grading:

```yaml
llm:
  evaluation:
    model: deepseek-chat
    url: https://api.deepseek.com/v1
  grading:
    model: deepseek-chat
    url: https://api.deepseek.com/v1
```

- **Multiple providers** — if one provider is down, fall back to another

## Runtime config (no restart needed)

- Move settings that currently require a restart into the DB or an admin
  page: language, exam parameters, active question bank
- Server-level settings (addr, DB path, LLM credentials) stay as
  flags/env — those legitimately need a restart

## Priority notes

- Question reload is the most immediate friction
  (currently requires deleting the DB volume)
- Exam profiles would be the biggest usability win
  for sharing with colleagues
- LLM split is more of a cost optimization for later
- Runtime config ties into the Phase 1 admin interface from the roadmap
