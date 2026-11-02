# Restore test, 2026-11-03

Record of the first test of the backup and restore procedure (ТЗ п. 3.7,
readiness criterion 8; issues #55, #56), on a separate stand.

## Result

**Passed** for the database: the counts before the backup and after the
restore are identical, and students, teachers and the admin work
normally on the restored system. **Passed** with the container image:
the command of the backup service and `scripts/restore.sh` were run
against a container set up like `examiner-ru`, with Docker in place of
podman. **Not yet tested** with rootless podman and systemd on
`examiner-01`; see
[What this test does not cover](#what-this-test-does-not-cover).

## Setup

| Item | Value |
| ---- | ----- |
| Stand | Windows 11 workstation, not `examiner-01`; two directories: `prod` (the source) and `stand` (the restore target) |
| Build | `examiner` built from branch `feature/issue-55-56-backup-restore` (base `main` at `c41ae9b`), Go 1.26.2 |
| Configuration | Russian instance settings: `lang: ru`, `base-path: /ru`, `num-questions: 2`, `questions/physics_ru.json` |
| LLM | A local OpenAI-compatible stub that scores every answer 4 of 5 |
| Data | Created through the web UI: admin, 1 teacher, 2 students; student 1 answered and submitted an exam, and the teacher confirmed a final grade of 85; student 2 has an exam in progress with one saved answer |

## Steps and results

| # | Step | Expected | Result |
| - | ---- | -------- | ------ |
| 1 | `examiner stats` on the source, server running | Counts printed | users 4, questions 56, sessions 2 (1 in progress, 1 reviewed), messages 6, scores 2, grades 1, final grades 1 |
| 2 | Backup from **Admin → Backups**, server running | Backup created and listed | `examiner-ru-20261102-231854.db.gz`, page says "created and checked" |
| 3 | `examiner backup` with `--config`, server running | Database and configuration archives, exit 0 | `…-231901.db.gz` (integrity ok) and `…-231901.config.tar.gz`, exit 0 |
| 4 | `examiner restore` of the admin-page backup into the empty `stand` | integrity ok, exit 0 | integrity ok, exit 0, 0.10 s |
| 5 | `examiner stats` on `stand`, compared with step 1 | Identical | **Identical** (`diff` reports no difference) |
| 6 | Contents of the configuration archive | Config files only, no `*.env` | `examiner.yaml`, `question_schema.json` |
| 7 | Server on the restored database, log in as each role | All pages work | `/ru/healthz` ok; admin: user list has all users, **Backups** opens; teacher: **Review** opens, session 1 shows the answers and the final grade 85; student 1: results page opens; student 2: the exam in progress shows the saved answer |
| 8 | Export from the restored database | Results of both sessions | `examiner export` (JSON): 2 results, statuses `reviewed` and `in_progress`. The CSV/Excel sheet (#53) does not exist yet |
| 9 | In-place restore on the source after a change made after the backup; server killed (no clean shutdown, `-wal` left) | Counts as in the backup; old files kept | Counts identical to the backup; `examiner.db.pre-restore-20261102-232030` kept with its `-wal` and `-shm`, and it still has the 5th user; 0.11 s |
| 10 | Start the server on the restored source | Starts, login works | `/ru/healthz` ok after 0.1 s; admin login works; the user added after the backup is gone, as expected |
| 11 | Restore from a damaged (truncated) backup | Refused, nothing changed | `Error: restore: read broken.db.gz: unexpected EOF`, exit 1; counts unchanged; nothing moved aside |
| 12 | Three backups with `--keep 2` | 2 newest kept | `…-232051` and `…-232053` kept, the oldest deleted |
| 13 | Backup of a missing database | Non-zero exit | `Error: open database: …`, exit 1 |
| 14 | Undo the restore of step 9: restore from `examiner.db.pre-restore-20261102-232030` | Back to the state before step 9 | 5 users again: the change that was only in the `-wal` of the replaced file is back |

Two things observed along the way:

- The first CLI backup in step 3 ran in the same second as the
  admin-page backup and was refused instead of overwriting it, since
  both names were the same. It was repeated a second later.
- On Windows the backup files show mode `rw-r--r--`. Windows has no
  `0600` mode; on Linux they are created `0600`, which the automated tests
  check.

## Container test (Docker Desktop)

The same procedure was then run with the container image, as close to
`examiner-01` as a workstation allows.

| Item | Value |
| ---- | ----- |
| Image | Built from this branch with the project `Dockerfile` (Docker 27.4.0, Linux engine); runs as `uid=999(examiner)` |
| Instance | Container `systemd-examiner-ru` started with the mounts, environment and port of `deploy/examiner-ru.container`: volume `systemd-examiner-ru-data` at `/data/db`, `127.0.0.1:8081`, base path `/ru`, `EXAMINER_BACKUP_DIR=/data/backups` |
| Host | A directory standing in for the host's home: Quadlet files, `~/examiner/examiner-ru.yaml`, `questions-ru.json`, `examiner-ru.env` with an LLM key, `/etc/caddy/Caddyfile` |
| podman, systemctl | Test shims: `podman run` → `docker run` with the same flags (`:U` dropped, Docker has no such option); `podman unshare CMD` → `CMD`; `systemctl --user start/stop examiner-ru` → `docker start/stop systemd-examiner-ru` |
| Data | Same scenario as above, through the web UI of the container |

| # | Step | Result |
| - | ---- | ------ |
| C1 | `examiner stats` in a second container on the instance's volume, instance running | users 4, questions 56, sessions 2 (1 in progress, 1 reviewed), messages 6, scores 2, grades 1, final 1 |
| C2 | **Admin → Backups → Create**, inside the container | Created and listed; file on the host in `~/examiner/backups/ru` |
| C3 | `ExecStartPre` and `ExecStart` of `examiner-backup@.service`, with `%h`, `%i` and `$$` expanded as systemd does, instance running | `Backup: /data/backups/examiner-ru-…-235027.db.gz (integrity ok)`, `Config: ….config.tar.gz`, exit 0. The archive holds `examiner-ru.yaml`, `questions-ru.json`, `examiner-ru.container`, `examiner-ru-data.volume`, `Caddyfile`. The LLM key from `examiner-ru.env` occurs 0 times in it |
| C4 | A user created after the backup | users 5 |
| C5 | `scripts/restore.sh ru examiner-ru-…-235027.db.gz` | Printed 5 users, stopped the instance, restored (integrity ok), printed the counts **identical to C1**, started the instance, `/ru/healthz` ok, `/ru/login` 200. Every file in the volume belongs to `examiner:examiner`. Total 5 s |
| C6 | `restore.sh` with a truncated backup | `Error: restore: read …: unexpected EOF`, exit 1; the instance was started again on the old database (`/ru/healthz` ok, users unchanged) |
| C7 | Undo the restore with the commands in `docs/ops/restore.md` | 5 users again |
| C8 | Clean-up command from `docs/ops/restore.md` | All `examiner.db.pre-restore-*` files removed; instance healthy |

Found and fixed during this test:

- **The image did not build**, independent of the backup work. The
  `Dockerfile` installed `templ@latest`, which now needs Go 1.26, while
  the image has Go 1.25. Both Dockerfiles now install the templ version
  from `go.mod`.

Found, not fixed here:

- **The image from `main` cannot run `serve`.** The handler reads
  `schema/question_schema.json` from the working directory, and the
  runtime image has no such file:
  `create handler: compile question schema: open /schema/question_schema.json`.
  PR #75 removes this file read. Until it merges, an image built from
  `main` does not start. For this test the schema was mounted at
  `/schema`.
- Docker gives a new named volume to root, so the instance (uid 999)
  could not create the database until the volume was handed to uid 999.
  Podman hands an empty named volume to the container's user on first
  mount, so on `examiner-01` this step is not needed.
- On Windows, `curl.exe` cannot write to `/dev/null` when
  `MSYS_NO_PATHCONV=1` is set (needed for Docker paths). The last
  `curl` of `restore.sh` therefore exited with 23 in step C5, after
  printing HTTP 200. The same line exits 0 without that variable, and on
  Linux the variable does not exist.

## What this test does not cover

Repeat these on `examiner-01` (or a VM with rootless podman) before
relying on the schedule. Record the results as a new protocol:

1. Rootless podman specifics that Docker does not have: the `:U` mount
   and the subuid owner of `~/examiner/backups/<lang>`, `podman unshare`,
   `label=disable` next to a `:Z` volume under SELinux, and the
   `systemd-examiner-<lang>-data` volume name that Quadlet creates.
1. systemd itself: `systemctl --user start examiner-backup@ru.service`
   waiting for the oneshot, the timer schedule
   (`systemctl --user list-timers`), and the real `examiner-ru` service in
   `restore.sh`.
1. The `task` commands over SSH: `task backup`, `task restore`, and
   `task deploy` / `task deploy-quick` stopping when the backup fails.
1. `task backup-pull` and an off-host copy.
1. The ведомость export (#53), once it exists.

The automated tests (`go test ./internal/backup/ ./cmd/examiner/
./internal/handler/`) cover the same database steps on every build. They
include a backup taken while 50 goroutines write messages, which passes
`integrity_check`.
