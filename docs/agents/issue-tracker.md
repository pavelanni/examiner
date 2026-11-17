# Issue tracker: GitHub

Issues and specs for this repo live as GitHub issues. Use the `gh` CLI for all operations.

## Conventions

- **Create an issue**: `gh issue create --title "..." --body "..."`. Use a heredoc for multi-line bodies.
- **Read an issue**: `gh issue view <number> --comments`, filtering comments by `jq` and also fetching labels.
- **List issues**: `gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title,
  body, labels: [.labels[].name], comments: [.comments[].body]}]'` with appropriate `--label` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> --body "..."`
- **Apply / remove labels**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **Close**: `gh issue close <number> --comment "..."`

Infer the repo from `git remote -v` — `gh` does this automatically when run inside a clone.

## Before merging a pull request

Check the current head SHA and base branch immediately before merging. Run
the relevant tests and required repository checks on that version. The Go
workflow runs templ generation, tests, race detection, vet, lint, and Linux
and Windows builds. Keep local validation evidence in the PR when a check
cannot run on GitHub; describe its actual result and limitation.

Read the review summary and every actionable comment, including review
threads and findings embedded in summary comments. Verify each finding and
fix it or explain with concrete evidence why it does not apply. Check
outdated threads too: a newer commit does not by itself prove a fix.

A successful CodeRabbit check means its run completed. It does not mean
that tests ran, all files were available, no findings remain, or a formal
approval was granted. An `Inconclusive` result, skipped diff, review limit,
or unreadable file is not a pass for that part of the review. Obtain the
missing review or resolve the uncertainty with evidence before merging.

When CodeRabbit reports that manual review is required, post
`@coderabbitai full review` in a PR comment and wait for the actual report.
The repository configuration enables approval after a completed review,
excludes skipped reviews, and prevents authors from forcing their own approval.

Distinguish a formal GitHub `APPROVED` review from a successful check or a
chat reply. When the user or repository rules require approval, verify the
actual review state. Otherwise report the review state accurately; do not
describe `COMMENTED` or a chat reply as an approval.

After further code or base changes, check which tests and review conclusions
became outdated and update them. Squash merges are enabled in this repository.
Specify the verified PR number, repository and head explicitly with a
non-interactive strategy:
`gh pr merge <number> --repo pavelanni/examiner --squash --match-head-commit <sha>`.
Recheck permitted strategies if the repository rules change, and follow the
normal repository requirements.

## Pull requests as a triage surface

**PRs as a request surface: no.** _(Set to `yes` if this repo treats external PRs as feature requests; `/triage` reads
this flag.)_

When set to `yes`, PRs run through the same labels and states as issues, using the `gh pr` equivalents:

- **Read a PR**: `gh pr view <number> --comments` and `gh pr diff <number>` for the diff.
- **List external PRs for triage**: `gh pr list --state open --json
  number,title,body,labels,author,authorAssociation,comments` then keep only `authorAssociation` of `CONTRIBUTOR`,
  `FIRST_TIME_CONTRIBUTOR`, or `NONE` (drop `OWNER`/`MEMBER`/`COLLABORATOR`).
- **Comment / label / close**: `gh pr comment`, `gh pr edit --add-label`/`--remove-label`, `gh pr close`.

GitHub shares one number space across issues and PRs, so a bare `#42` may be either — resolve with `gh pr view 42` and
fall back to `gh issue view 42`.

## When a skill says "publish to the issue tracker"

Create a GitHub issue.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --comments`.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a single issue with **child** issues as tickets.

- **Map**: a single issue labelled `wayfinder:map`, holding the Notes / Decisions-so-far / Fog body. `gh issue create
  --label wayfinder:map`.
- **Child ticket**: an issue linked to the map as a GitHub sub-issue (`gh api` on the sub-issues endpoint). Where
  sub-issues aren't enabled, add the child to a task list in the map body and put `Part of #<map>` at the top of the
  child body. Labels: `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). Once claimed, the ticket is
  assigned to the driving dev.
- **Blocking**: GitHub's **native issue dependencies** — the canonical, UI-visible representation. Add an edge with
  `gh api --method POST repos/<owner>/<repo>/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>`,
  where `<blocker-db-id>` is the blocker's numeric **database id** (`gh api repos/<owner>/<repo>/issues/<n> --jq .id`,
  _not_ the `#number` or `node_id`). GitHub reports `issue_dependencies_summary.blocked_by` (open blockers only — the
  live gate). Where dependencies aren't available, fall back to a `Blocked by: #<n>, #<n>` line at the top of the
  child body. A ticket is unblocked when every blocker is closed.
- **Frontier query**: list the map's open children (`gh issue list --state open`, scoped to the map's sub-issues /
  task list), drop any with an open blocker (`issue_dependencies_summary.blocked_by > 0`, or an open issue in the
  `Blocked by` line) or an assignee; first in map order wins.
- **Claim**: `gh issue edit <n> --add-assignee @me` — the session's first write.
- **Resolve**: `gh issue comment <n> --body "<answer>"`, then `gh issue close <n>`, then append a context pointer
  (gist + link) to the map's Decisions-so-far.
