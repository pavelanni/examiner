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

### Deploy workflow

```bash
task deploy          # build amd64, push to ghcr.io, update remote, restart
task deploy-quick    # update tag + restart (no rebuild)
task deploy-quick DEV_TAG=abc1234   # rollback to specific version
task deploy-status   # check remote service status
task deploy-logs     # show remote logs
```

### How deploy works

1. Builds `linux/amd64` image on Mac (cross-platform via Podman)
1. Tags as `ghcr.io/pavelanni/examiner:<git-sha>`
1. Pushes to ghcr.io
1. SSHes to `examiner-01`, updates `Image=` line in
   `~/.config/containers/systemd/examiner.container` via `sed`
1. Runs `systemctl --user daemon-reload && systemctl --user restart examiner`

### Key deployment files

| File | Purpose |
| ---- | ------- |
| `deploy/examiner.container` | Podman Quadlet systemd unit |
| `deploy/examiner-data.volume` | Named volume for SQLite database |
| `deploy/examiner.env.example` | Environment variable template |
| `deploy/Caddyfile` | Caddy reverse proxy config |

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
