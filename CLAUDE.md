# Examiner - project instructions

## Build and run

- Build tool: [Task](https://taskfile.dev/) (`Taskfile.yml`), not Make
- Template generation: `templ generate` (required before build)
- Build: `task build` (runs templ generate automatically)
- Run locally: `task run -- --lang ru --shuffle`
- Tests: `task test`
- Lint: `task lint` (requires golangci-lint)

## Deployment

- **Registry**: `ghcr.io/pavelanni/examiner`
- **Cloud host**: `examiner-01` (SSH alias), domain `examiner.pavelanni.dev`
- **Image tagging**: 7-character git SHA (e.g. `2b1cf1b`), with `-dirty` suffix for uncommitted changes
- **Runtime**: Podman Quadlet + systemd (user services)
- **Reverse proxy**: Caddy with auto-TLS

### Dual-language setup

Two container instances run behind Caddy with path-based routing:

- `/` and `/*` -> English instance (port 8080)
- `/ru/` and `/ru/*` -> Russian instance (port 8081)

Each instance has its own database, questions file, and env config.

### Deploy workflow

```bash
task deploy          # build amd64, push to ghcr.io, update both instances
task deploy-quick    # update tag + restart both (no rebuild)
task deploy-quick DEV_TAG=abc1234   # rollback to specific version
task deploy-status   # check both service statuses
task deploy-logs     # show logs from both (or LANG=en / LANG=ru)
```

### How deploy works

1. Builds `linux/amd64` image on Mac (cross-platform via Podman)
1. Tags as `ghcr.io/pavelanni/examiner:<git-sha>`
1. Pushes to ghcr.io
1. SSHes to `examiner-01`, updates `Image=` line in both
   `examiner-en.container` and `examiner-ru.container` via `sed`
1. Runs `systemctl --user daemon-reload && systemctl --user restart examiner-en examiner-ru`

### Important notes

- **Commit before deploying.** The `DEV_TAG` includes a `-dirty` suffix
  when the working tree has uncommitted changes. If you run `task deploy`
  with a dirty tree, the pushed image is tagged e.g. `6cc8b31-dirty`.
  A subsequent `task deploy-quick` after committing will compute a
  *different* (clean) SHA and try to deploy a tag that doesn't exist
  on ghcr.io. Always commit first, or pass `DEV_TAG=<sha>` explicitly.
- **First push to ghcr.io.** New GitHub Container Registry packages
  default to **private**. After the first `task deploy`, go to
  `https://github.com/users/pavelanni/packages/container/examiner/settings`
  and change visibility to **Public** (or `podman login ghcr.io` on
  `examiner-01`).
- **Manual Quadlet edits on the host.** If you edit container files
  directly on `examiner-01`, you must run
  `systemctl --user daemon-reload` before restarting.
- **Questions files on host.** Each instance mounts a separate questions
  file: `~/examiner/questions-en.json` and `~/examiner/questions-ru.json`.

### Key deployment files

| File | Purpose |
| ---- | ------- |
| `deploy/examiner-en.container` | English instance Quadlet unit (port 8080) |
| `deploy/examiner-ru.container` | Russian instance Quadlet unit (port 8081) |
| `deploy/examiner-en-data.volume` | English database volume |
| `deploy/examiner-ru-data.volume` | Russian database volume |
| `deploy/examiner-en.env.example` | English env template |
| `deploy/examiner-ru.env.example` | Russian env template |
| `deploy/Caddyfile` | Caddy reverse proxy with path routing |

## Project structure

- `cmd/examiner/` — entry point, CLI flags
- `internal/handler/` — HTTP handlers (chi router) + Templ views
- `internal/i18n/` — internationalization (English, Russian)
- `internal/llm/` — OpenAI-compatible LLM client
- `internal/model/` — domain types
- `internal/store/` — SQLite storage layer
- `web/` — static assets (htmx, Pico CSS)

## Tech notes

- Go 1.25, pure Go SQLite (modernc.org, no CGO)
- Frontend: htmx 2.0 + Pico CSS v2 (no SPA framework)
- LLM: any OpenAI-compatible endpoint (Ollama, vLLM, OpenAI API)
