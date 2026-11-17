# Route access (RBAC)

Who may call each route of the exam server, and what the handler checks
beyond the role. Tests: `internal/handler/rbac_test.go`.

Anonymous requests to any route except `/login` are redirected to `/login`
(htmx requests get 401 with `HX-Redirect`). `requireRole` answers 403 to a
signed-in user without the role.

| Route | Roles | Handler checks |
| ----- | ----- | -------------- |
| `GET /login`, `POST /login` | anyone | — |
| `POST /logout` | any signed-in | — |
| `GET /` | any signed-in | students see only their own sessions |
| `POST /exam/start` | any signed-in | session is created for the caller |
| `GET /exam/{sessionID}` | any signed-in | student: owner only (403) |
| `POST /exam/{sessionID}/answer/{threadID}` | any signed-in | student: owner only (403); thread belongs to session (403); session `in_progress` (400); thread not completed (409) |
| `POST /exam/{sessionID}/submit` | any signed-in | student: owner only (403); session `in_progress` (409) |
| `GET /results/{sessionID}` | any signed-in | owner only, for every role (403); what is shown depends on `score_visibility` |
| `GET /review` | teacher, admin | lists `graded` and `reviewed` sessions |
| `GET /review/{sessionID}` | teacher, admin | — |
| `POST /review/{sessionID}/score/{threadID}` | teacher, admin | session `graded` (409); thread belongs to session (403); `0 ≤ score ≤ max_points` (400) |
| `POST /review/{sessionID}/finalize` | teacher, admin | session `graded` (409), so a second confirmation cannot overwrite the reviewer; `0 ≤ grade ≤ 100` (400) |
| `GET /teacher/me`, `/teacher/profile`, `/teacher/create-test` | teacher, admin | files filtered by owner |
| `POST /teacher/tests` | teacher, admin | editing an existing file: owner only (403) |
| `GET /teacher/tests/file/{name}` | teacher, admin | owner only |
| `GET/POST /admin/users`, `POST /admin/users/{userID}/toggle` | admin | — |
| `GET/POST /admin/questions` | admin | — |

## Status codes for IDs in the URL

- 400: the ID is not a positive integer.
- 404: no record with that ID.
- 403: the record exists but belongs to someone else.
- 409: the record is in the wrong status for the action.

Internal errors return a generic message; the detail goes to the log only.

## Known limits

- The status checks in score update and finalize are read-then-write, not
  an atomic transition: two teachers confirming the same session at the same
  moment can both pass the check. The planned `SessionStore.Transition`
  (docs/development/runtime-design.md) closes this.
- Whether students see LLM scores before teacher review is configured by
  `score_visibility` (`final` by default, `none` hides them until review).
