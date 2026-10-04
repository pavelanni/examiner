# Next session plan

Written 2026-10-04, after the score visibility PR (#68) and the skills setup
(#69). It continues the migration plan in [runtime-design.md](runtime-design.md).

## Where we are

| Step in the migration plan | Status |
| -------------------------- | ------ |
| Prompts and follow-up limit (#37, #39) | Merged (#64) |
| SQLite pragmas, WAL, foreign keys (#40) | Merged (#67) |
| `score_visibility` flag | Merged (#68), default `final` |
| `SessionStore` interface and SessionSpec types | Next |

## Quick cleanup first

| Item | What to do |
| ---- | ---------- |
| #38 | The LLM contradicting the student's answer. #64 may have fixed it. Read the code, then close it or say what is left. |
| #65 | Follow-ups from #64: single owner for the follow-up count, answer serialization across replicas, truncation floor. It overlaps the `SessionStore` work, so decide whether to fold it in. Don't implement it first. |
| #61 | User-facing error when CSRF validation fails. Small and independent. |

## Main task: `SessionStore` and SessionSpec

This changes interfaces that both tracks build on, so it is architectural:
brainstorm, written spec in `docs/superpowers/specs/`, plan, implementation.

Read first: the Persistence and Grading sections of `runtime-design.md` and
the Hindsight page on the re-architecture. `/grill-with-docs` is a good way
to run the brainstorm, and it creates the first `CONTEXT.md` from the terms
that get settled.

Decisions to settle:

1. Where the types live. Proposal: SessionSpec and the interface in
   `internal/model`, and the SQLite implementation wrapping `store.Store`.
1. `Transition(id, from, to)` semantics: an atomic status change with a
   precondition, plus a fencing token for grading attempts (CodeRabbit's
   review of #63).
1. `AppendMessage` idempotency by message key (#49).
1. What replaces the in-process `inFlight` answer lock from #64 across
   replicas (this is #65).
1. Slicing: types and interface, then the SQLite implementation, then move
   handler groups off `store.Store` one at a time, so each PR stays small.

## Reminders

- **First deploy of #67 and #68.** Check the logs for `dangling foreign key`
  warnings. WAL adds `-wal` and `-shm` files, so copying only the `.db` file
  is not a safe backup (#55).
- **Review process.** CodeRabbit allows one included review per hour. Run an
  external model review first, and spend the CodeRabbit review once per PR.
- **Skills.** `/triage` will create four missing labels on first use
  (`docs/agents/triage-labels.md`).
- **Commit trailer.** The user's rule is `Assisted-By`, never `Co-Authored-By`.
  The harness may suggest the latter. The rule wins.
