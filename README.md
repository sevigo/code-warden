# Code-Warden

[![Go Report Card](https://goreportcard.com/badge/github.com/sevigo/code-warden)](https://goreportcard.com/report/github.com/sevigo/code-warden)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A self-hosted GitHub App that reviews pull requests with full codebase context — not just the diff.

Why does that matter? Most AI review tools only see what changed. Code-Warden runs the review through an agent that investigates the actual repository — using grep and reading files to understand the code surrounding the diff — so it can catch issues the diff alone hides. It runs four parallel review angles (bug, security, performance, conventions) on the same model.

Everything runs on your infrastructure. Your code never leaves.

---

## Quick Start

### Standalone review (fastest, no GitHub setup)

```sh
go run ./cmd/review --local .           # review uncommitted changes in a local repo
go run ./cmd/review --local . --base main   # review a branch vs main
go run ./cmd/review --pr owner/repo --number 123   # review a public PR
```

The review model defaults to Ollama (e.g. `ornith:9b`). Point it at any model:

```sh
AI_GENERATOR_MODEL=ornith:9b go run ./cmd/review --local .
```

Add `--json` for machine-readable output or `--no-color` for plain text.

Human-readable and `--prompt-only` reviews end with a coverage receipt showing which changed files were reviewed, which were ignored, and whether each configured review angle completed, returned a partial response, or was skipped. A `PARTIAL` receipt means a zero-finding result must not be treated as a fully clean review.

To inspect and compare agent-loop behavior, save a private trace for each run:

```sh
go run ./cmd/review --local . --trace-dir ./review-traces
```

Each invocation creates a timestamped directory containing:

- `manifest.json` — safe model identity, review options, timing, changed files, and per-angle token/iteration metrics
- `input.diff` — the exact diff supplied to the reviewer
- `review.json` and `review.xml` — the merged structured review
- `angle-*.raw.txt` — the raw final response from each review angle

Trace directories and files are created with private permissions. Configured API keys, GitHub tokens, clone URLs, workspace paths, and provider endpoints are not written, but traces do contain source code and model output; do not commit or share them without reviewing their contents.

### Full server (15 minutes, includes web UI)

```sh
git clone https://github.com/sevigo/code-warden
cd code-warden
make quickstart             # guided interactive setup
```

Starts everything in Docker with a web dashboard at `localhost:8080`. The wizard checks prerequisites, configures `.env`, detects your GPU, and pulls a local model. The review model (`kimi-k2.5`) runs as an Ollama cloud model — no GPU needed for that.

**GPU support** (optional — CPU works fine for demos):
```sh
# NVIDIA
docker compose -f docker-compose.demo.yml -f docker-compose.gpu.yml up -d

# AMD ROCm
docker compose -f docker-compose.demo.yml -f docker-compose.amd.yml up -d
```

**Handy commands:**
```sh
make demo-logs    # tail server logs
make demo-down    # stop all services
make demo-up      # restart services
make pull-models  # pull models to host Ollama (outside Docker)
```

**Prerequisites:** Docker, Go 1.22+

---

## How It Works

When someone comments `/review` on a PR:

1. **Sync** — clone or update the repo to the PR's default branch
2. **Investigate** — the agent clones the PR's checkout and runs grep + `read_file` to understand the diff in context
3. **Review** — parallel agent passes (bug, security, performance, conventions) each review the diff with read-only tools, then merge + dedupe findings
4. **Post** — severity-rated findings as inline GitHub comments

`/rereview` is an alias for `/review` — it always reviews the current diff fresh.

---

## Features

**Reviews**
- Agent-based — investigates the diff with grep + read_file against an isolated checkout
- Multi-angle — bug, security, performance, and conventions passes run in parallel on the same model
- Diff-boundary enforced — findings outside the changed hunks are dropped
- Structured output — severity badges (🔴 critical · 🟠 warning · 🟡 suggestion) with inline comments

**Infrastructure**
- Self-hosted — Ollama (local) or cloud LLMs (Gemini, any OpenAI-compatible endpoint)
- PostgreSQL for job history and review storage
- Per-repository config via `.code-warden.yml`

---

## GitHub App Setup

Required for full server mode (webhook-triggered reviews on PRs).

1. Create a new GitHub App in your organization settings
2. Set the webhook URL to `https://your-host/api/v1/webhook/github`
3. Request permissions: `Pull requests: Read & Write`, `Issues: Read & Write`, `Contents: Read`
4. Subscribe to events: `Pull request`, `Issue comment`, `Push`
5. Generate and download a private key → save to `keys/`
6. Install the app on the repositories you want reviewed

Add credentials to `.env`:

```sh
GITHUB_APP_ID=12345
GITHUB_WEBHOOK_SECRET=your-secret
GITHUB_PRIVATE_KEY_PATH=keys/app.private-key.pem
```

Or in `config.yaml`:

```yaml
github:
  app_id: 12345
  webhook_secret: "your-secret"
  private_key_path: "keys/app.private-key.pem"
```

---

## Configuration

### Application (`config.yaml`)

```yaml
ai:
  llm_provider: "ollama"           # "ollama", "gemini", or "openai"
  ollama_host: "http://localhost:11434"
  generator_model: "kimi-k2.5:cloud"
```

### Per-repository (`.code-warden.yml`)

```yaml
custom_instructions:
  - "This is a financial system — flag any missing input validation"
  - "We use repository pattern; flag direct DB access in service layer"

exclude_dirs:
  - vendor
  - node_modules
```

Full reference: [config.yaml.example](config.yaml.example)

---

## Documentation

| Document | Description |
|---|---|
| [docs/SETUP.md](docs/SETUP.md) | Deployment and first-run guide |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Component relationships and system design |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Common issues and fixes |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to contribute |

---

## Built On

Code-Warden is built on [GoFrame](https://github.com/sevigo/goframe), a Go framework that provides LLM providers, agent loops, and tool calling.

## License

MIT — see [LICENSE](LICENSE).
