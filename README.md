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

Open `http://localhost:8080` in a browser.

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
```

For architecture details, data flow diagrams, and database schema,
see `docs/architecture.md`.

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

### Container image

```bash
task image
podman push localhost/examiner:latest registry.example.com/examiner:latest
```

### Quadlet (Podman + systemd)

On the target server:

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
systemctl --user status examiner
journalctl --user -u examiner -f
```

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
