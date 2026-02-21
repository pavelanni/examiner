# Examiner

LLM-based student assessment system.
A student answers open-ended questions in a browser,
an LLM evaluator provides real-time feedback and follow-up questions,
then grades the entire exam. A teacher can review and override scores.

## Features

- **Conversational exam flow** — the LLM can ask follow-up questions
  to probe deeper understanding (configurable limit per question)
- **Automated grading** — LLM scores each answer against a rubric
  and model answer, then produces an overall grade
- **Teacher review** — teachers can adjust per-question scores,
  add comments, and finalize the grade
- **i18n support** — UI available in English and Russian;
  adding a new language requires only a JSON file
- **Any OpenAI-compatible LLM** — works with Ollama, vLLM,
  OpenAI API, or any provider that speaks the OpenAI chat protocol

## Quick start

### Prerequisites

- Go 1.25+
- [Templ CLI](https://templ.guide/) (`go install github.com/a-h/templ/cmd/templ@latest`)
- An OpenAI-compatible LLM endpoint
  (e.g. [Ollama](https://ollama.com/) running locally)

### Build and run

```bash
templ generate
go build -o examiner ./cmd/examiner/

# With Ollama (default settings)
./examiner --questions questions.json

# With a remote API
./examiner \
  --llm-url https://api.openai.com/v1 \
  --llm-key "$OPENAI_API_KEY" \
  --llm-model gpt-4o \
  --questions questions.json
```

On first run, note the generated admin password printed to stderr
(or set one with `--admin-password`).
Open `http://localhost:8080` and log in as `admin`.

### Configuration

Settings can be provided via CLI flags, environment variables,
or a config file. Precedence: **flags > env vars > config file > defaults**.

#### CLI flags

| Flag | Short | Default | Description |
| ---- | ----- | ------- | ----------- |
| `--addr` | `-a` | `:8080` | HTTP listen address |
| `--db` | | `examiner.db` | SQLite database path |
| `--questions` | `-q` | `questions.json` | Path to questions JSON file |
| `--llm-url` | | `http://localhost:11434/v1` | OpenAI-compatible API base URL |
| `--llm-key` | | `ollama` | API key for the LLM |
| `--llm-model` | | `llama3.2` | Model name |
| `--lang` | `-l` | `en` | UI language (`en`, `ru`) |
| `--num-questions` | `-n` | `0` (all) | Number of questions per exam |
| `--difficulty` | `-d` | (all) | Filter by difficulty (`easy`, `medium`, `hard`) |
| `--topic` | `-t` | (all) | Filter by topic |
| `--max-followups` | | `3` | Max follow-up questions per answer |
| `--shuffle` | | `false` | Randomize question order |
| `--admin-password` | | (generated) | Initial admin password (first run only) |

#### Environment variables

All flags can be set via environment variables with the `EXAMINER_`
prefix. Hyphens become underscores:

```bash
export EXAMINER_LLM_URL=https://api.openai.com/v1
export EXAMINER_LLM_KEY=sk-...
export EXAMINER_LLM_MODEL=gpt-4o
export EXAMINER_LANG=ru
export EXAMINER_NUM_QUESTIONS=10
export EXAMINER_SHUFFLE=true
export EXAMINER_ADMIN_PASSWORD=changeme
```

#### Config file

Place an `examiner.yaml` (or `.toml`, `.json`) in the working
directory, `~/.config/examiner/`, or `/etc/examiner/`:

```yaml
addr: ":8080"
db: examiner.db
questions: questions_physics_ru.json
lang: ru
llm-url: https://api.openai.com/v1
llm-key: sk-...
llm-model: gpt-4o
num-questions: 10
difficulty: medium
shuffle: true
max-followups: 3
```

#### Examples

```bash
# 10 random medium-difficulty questions in Russian
./examiner -l ru -n 10 -d medium --shuffle

# Only "Mechanics" topic, 5 questions
./examiner -t "Законы Ньютона" -n 5 --shuffle
```

## Authentication and user management

The application requires login. On first run it creates a default
**admin** user.

### Setting the admin password

Provide a password via the `--admin-password` flag or the
`EXAMINER_ADMIN_PASSWORD` environment variable:

```bash
# Via flag
./examiner --admin-password secret123 --questions questions.json

# Via environment variable
EXAMINER_ADMIN_PASSWORD=secret123 ./examiner --questions questions.json
```

If neither is set, the server generates a random 16-character password
and prints it to stderr on first startup:

```text
========================================
  Generated admin password: aB3xK9mPqR7wZ2nL
========================================
```

The admin user is only created once (when the `users` table is empty).
Changing the flag or variable on subsequent runs has no effect.

### Adding users

Only admins can create users. Log in as `admin`, then navigate to
**Admin → User management** (`/admin/users`). The form lets you set:

- **Username** — used for login
- **Display name** — shown in the UI (defaults to username if empty)
- **Password** — bcrypt-hashed, stored in the database
- **Role** — `student`, `teacher`, or `admin`

From the same page you can toggle a user's active status (deactivated
users cannot log in).

### Roles

| Role | Permissions |
| ---- | ----------- |
| `student` | Take exams, view own sessions and results |
| `teacher` | Everything students can do, plus review and grade any exam |
| `admin` | Everything teachers can do, plus manage users and upload questions |

### Uploading questions via the admin UI

Admins can upload question JSON files at **Admin → Question upload**
(`/admin/questions`). The file format is the same as the `--questions`
flag (see below). Duplicate files (matching SHA-256 hash) are rejected.

## Question format

Questions are loaded from a JSON file on first startup
(when the database is empty). See `questions.json` for the
Kubernetes RBAC example or `questions_physics_ru.json` for a
Russian-language high school physics set.

```json
[
  {
    "text": "Explain Newton's second law.",
    "difficulty": "easy",
    "topic": "Mechanics",
    "rubric": "Should state F=ma, explain each variable...",
    "model_answer": "Newton's second law states that...",
    "max_points": 10
  }
]
```

| Field | Description |
| ----- | ----------- |
| `text` | The question shown to the student |
| `difficulty` | `easy`, `medium`, or `hard` |
| `topic` | Topic label displayed in the UI |
| `rubric` | Grading criteria (sent to the LLM, hidden from student) |
| `model_answer` | Reference answer (sent to the LLM, hidden from student) |
| `max_points` | Maximum score for this question |

## Project structure

```text
cmd/examiner/          Entry point, CLI flags, question loading
internal/
  handler/             HTTP handlers (chi router)
    views/             Templ components (layout, pages, fragments)
  i18n/                Internationalization (go-i18n v2)
    locales/           Translation files (active.en.json, active.ru.json)
  llm/                 OpenAI-compatible LLM client
  model/               Domain types (Question, Session, Thread, etc.)
  store/               SQLite storage layer with auto-migration
deploy/
  examiner.container   Podman Quadlet unit
  examiner-data.volume Quadlet named volume
  examiner.env.example Environment file template
  Caddyfile            Caddy reverse proxy config
docs/
  architecture.md      System design, data flow, database schema
  development/
    roadmap.md         Demo → PoC → MVP → production roadmap
```

For architecture details see `docs/architecture.md`.
For the development roadmap see `docs/development/roadmap.md`.

## Adding a new language

1. Copy `internal/i18n/locales/active.en.json` to `active.XX.json`
1. Translate the `"other"` (and plural) values
1. Rebuild: `templ generate && go build ./cmd/examiner/`
1. Run with `--lang XX`

Russian plurals (one/few/many/other) are fully supported via CLDR rules.

## Building with Taskfile

The project uses [Task](https://taskfile.dev/) instead of Make:

```bash
task build           # generate + build
task run             # build + run (pass flags via -- : task run -- -l ru)
task test            # run tests
task image           # build container image with Podman
task image-run       # run container locally
task image-amd64     # build x86_64 image for cloud (tagged with git SHA)
task push            # push image to ghcr.io
task release         # build amd64 + push in one step
task deploy          # full pipeline: build, push, update remote, restart
task deploy-quick    # update remote + restart (no rebuild)
task deploy-status   # check service status on cloud host
task deploy-logs     # show recent logs from cloud host
task clean           # remove build artifacts
task --list-all      # show all available tasks
```

## Running in a container locally

Build the image and run with Podman (or Docker):

```bash
# Build the image.
task image
# — or without Task:
podman build -t examiner:latest .
```

The container needs:

- A **volume** for the SQLite database (so data survives restarts)
- A **bind mount** for the questions file
- **Environment variables** for LLM credentials

```bash
# Create a named volume for the database.
podman volume create examiner-data

# Run with Ollama on the host.
podman run --rm -it \
  -p 8080:8080 \
  -v examiner-data:/data/db:Z \
  -v ./questions.json:/data/questions.json:ro,Z \
  -e EXAMINER_DB=/data/db/examiner.db \
  -e EXAMINER_QUESTIONS=/data/questions.json \
  -e EXAMINER_LLM_URL=http://host.containers.internal:11434/v1 \
  -e EXAMINER_LANG=ru \
  -e EXAMINER_ADMIN_PASSWORD=changeme \
  examiner:latest

# Run with a remote API (e.g. OpenAI).
podman run --rm -it \
  -p 8080:8080 \
  -v examiner-data:/data/db:Z \
  -v ./questions_physics_ru.json:/data/questions.json:ro,Z \
  -e EXAMINER_DB=/data/db/examiner.db \
  -e EXAMINER_QUESTIONS=/data/questions.json \
  -e EXAMINER_LLM_URL=https://api.openai.com/v1 \
  -e EXAMINER_LLM_KEY=sk-... \
  -e EXAMINER_LLM_MODEL=gpt-4o \
  -e EXAMINER_LANG=ru \
  -e EXAMINER_NUM_QUESTIONS=10 \
  -e EXAMINER_SHUFFLE=true \
  examiner:latest
```

Or use `task image-run` as a shortcut (uses Ollama defaults):

```bash
task image-run
task image-run -- --lang ru --shuffle
```

To inspect or back up the database:

```bash
# Find where the volume lives.
podman volume inspect examiner-data --format '{{.Mountpoint}}'

# Or copy the database out of the volume.
podman volume export examiner-data | tar -xf - db/examiner.db
```

To start fresh, remove the volume:

```bash
podman volume rm examiner-data
```

## Deploying to a server

See `deploy/` for all deployment files.
Images are pushed to `ghcr.io/pavelanni/examiner` and tagged with the
7-character git SHA (e.g. `2b1cf1b`).

### Initial server setup

On the target server (one-time):

```bash
# Prepare directories and config.
mkdir -p ~/examiner
cp questions.json ~/examiner/
cp deploy/examiner.env.example ~/examiner/examiner.env
# Edit examiner.env with your LLM API key.

# Install Quadlet units.
mkdir -p ~/.config/containers/systemd
cp deploy/examiner.container deploy/examiner-data.volume \
   ~/.config/containers/systemd/

# Reload and start.
systemctl --user daemon-reload
systemctl --user start examiner
```

### Deploying updates from a Mac

The project builds x86_64 images on Apple Silicon and pushes to ghcr.io.
The deploy task then SSHes to the cloud host to update the image tag
and restart the service.

Prerequisites:

- `podman login ghcr.io` on your Mac
- SSH access to the cloud host (configured as `examiner-01` in `~/.ssh/config`)

```bash
# Full deploy: build amd64 image, push to ghcr.io, restart remote service.
task deploy

# Deploy a previously pushed image (no rebuild).
task deploy-quick

# Deploy a specific version (rollback).
task deploy-quick DEV_TAG=abc1234

# Check status and logs.
task deploy-status
task deploy-logs
```

The `deploy` task automatically tags the image with the current git SHA
and updates `~/.config/containers/systemd/examiner.container` on the
remote host via `sed`.

**Important:**

- **Commit before deploying.** The image tag includes a `-dirty` suffix
  when the working tree has uncommitted changes. If you deploy with a
  dirty tree and then commit, `deploy-quick` will compute a different
  (clean) SHA that doesn't exist on ghcr.io. Always commit first,
  or pass `DEV_TAG=<sha>` explicitly.
- **First push to ghcr.io.** New packages default to private. After the
  first push, go to the package settings on GitHub and change visibility
  to **Public** — otherwise the cloud host won't be able to pull.
- **Manual Quadlet edits on the host.** If you edit the `.container`
  file directly on `examiner-01`, run `systemctl --user daemon-reload`
  before restarting the service.

### Caddy reverse proxy

```bash
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
# Edit the domain name, then:
sudo systemctl reload caddy
```

Caddy automatically provisions TLS certificates via Let's Encrypt.

## Tech stack

| Component | Technology |
| --------- | ---------- |
| Language | Go 1.25 |
| Router | chi v5 |
| Configuration | pflag + Viper |
| Templates | Templ |
| Database | SQLite (modernc.org, pure Go) |
| LLM client | sashabaranov/go-openai |
| i18n | nicksnyder/go-i18n v2 |
| CSS | Pico CSS v2 |
| Frontend interactivity | htmx 2.0 |

## License

[MIT](LICENSE)
