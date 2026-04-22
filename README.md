# Code-Warden

[![Go Report Card](https://goreportcard.com/badge/github.com/sevigo/code-warden)](https://goreportcard.com/report/github.com/sevigo/code-warden)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A self-hosted GitHub App that reviews pull requests with full codebase context — not just the diff.

Why does that matter? Most AI review tools only see what changed. They don't know that `UserService` already exists in another package, that your team avoids a certain pattern, or that this change breaks three callers downstream. Code-Warden retrieves architectural context, resolves definitions, traces downstream impact, and includes commit history before the LLM ever sees the diff.

Everything runs on your infrastructure. Your code never leaves.

---

## Quick Start

### Demo mode (5 minutes, no GitHub App needed)

```sh
git clone https://github.com/sevigo/code-warden
cd code-warden
cp .env.example .env        # add your GitHub PAT to GITHUB_TOKEN
make demo PR=https://github.com/owner/repo/pull/42
```

Clones the repo, indexes it into local Qdrant, prints findings to the terminal. No webhook, no GitHub App, no public URL.

### Full server (15 minutes, includes web UI)

```sh
git clone https://github.com/sevigo/code-warden
cd code-warden
make quickstart             # guided interactive setup
```

Starts everything in Docker with a web dashboard at `localhost:8080`. The wizard checks prerequisites, configures `.env`, detects your GPU, and pulls two local models (~1.6 GB). The review model (`kimi-k2.5`) runs as an Ollama cloud model — no GPU needed for that.

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

1. **Sync** — clone or update the repo, re-index changed files into Qdrant
2. **Context retrieval** — five parallel stages:
   - *Architectural* — directory-level summaries for touched paths
   - *HyDE* — hypothetical document embeddings for semantic search
   - *Impact* — callers and importers of changed symbols
   - *Description* — code related to the PR title, body, and commits
   - *Definitions* — exact type/function definitions for every symbol in the diff
3. **Review** — context + diff + custom instructions go to the LLM (or multiple models in consensus mode)
4. **Post** — severity-rated findings as inline GitHub comments

`/rereview` runs a follow-up pass comparing the new diff against previous findings — what was fixed, what was missed, what's new.

`/implement` goes further: an agent reads the issue, explores the codebase via MCP tools, writes code, runs lint and tests, reviews its own work, and opens a PR.

---

## Features

**Reviews**
- Context-aware — retrieves relevant code before the LLM sees the diff
- Consensus mode — multiple models in parallel, synthesized into one review
- Re-review — checks whether previous findings were addressed
- Structured output — severity badges (🔴 critical · 🟠 warning · 🟡 suggestion) with inline comments

**Indexing**
- Incremental — only re-indexes files that changed in the diff
- Hybrid search — dense embeddings + code-aware sparse vectors
- Code-aware chunking — preserves function boundaries, propagates file-level metadata
- Multi-language AST — extracts definitions, imports, and structure

**Agent (`/implement`)**
- Reads a GitHub issue, plans, and implements changes in an isolated workspace
- MCP tools: `search_code`, `get_symbol`, `get_arch_context`, `review_code`, `push_branch`
- Self-review loop before committing
- Only reviewed files are included in the PR

**Infrastructure**
- Self-hosted — Ollama (local) or cloud LLMs via proxy
- PostgreSQL for job history and review storage
- Qdrant for vector storage
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
  llm_provider: "ollama"           # "ollama" or "gemini"
  ollama_host: "http://localhost:11434"
  generator_model: "kimi-k2.5:cloud"
  embedder_model: "qwen3-embedding:0.6b"
  comparison_models:               # Enable consensus review
    - "kimi-k2.5:cloud"
    - "deepseek-v3.1:671b-cloud"
  enable_reranking: true
  enable_hybrid_search: true
  context_token_budget: 16000
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

## CLI

```sh
# Manually re-index a repository
./bin/warden-cli update /path/to/repo

# Full prescan (initial index or forced rebuild)
./bin/warden-cli prescan /path/to/repo

# Review a PR from the command line
export CW_GITHUB_TOKEN="ghp_xxx"
./bin/warden-cli review https://github.com/owner/repo/pull/123
```

---

## Terminal UI (Onboarding Assistant)

Interactive terminal UI for exploring and querying indexed repositories — useful for developer onboarding, code exploration, and debugging.

```sh
make build-terminal
./bin/warden-term
```

### Themes

```sh
# Available: cyan, matrix, amber, cyberpunk, ice, dracula, fire
./bin/warden-term --theme matrix
CODE_WARDEN_THEME=dracula ./bin/warden-term
./bin/warden-term --list-themes
```

### Commands

| Command | Description |
|---------|-------------|
| `/add [name] [path]` | Register and index a local repository |
| `/list`, `/ls` | List registered repositories |
| `/select [name]` | Set active repository |
| `/rescan [name?]` | Re-scan for updates |
| `/new`, `/reset` | Start a new conversation |
| `/help`, `/h` | Show available commands |
| `/exit`, `/quit` | Exit |

1. `/add my-project /path/to/repo`
2. `/select my-project`
3. Ask questions freely: `How does authentication work?`, `What's the pattern for adding a new endpoint?`

The terminal uses the RAG pipeline to retrieve relevant code before answering — architectural summaries, function definitions, and dependency relationships, not just keyword matches.

---

## Documentation

| Document | Description |
|---|---|
| [docs/SETUP.md](docs/SETUP.md) | Deployment and first-run guide |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Component relationships and system design |
| [docs/INDEXING.md](docs/INDEXING.md) | Chunk types, metadata, debugging retrieval |
| [docs/IMPLEMENT_ARCHITECTURE.md](docs/IMPLEMENT_ARCHITECTURE.md) | `/implement` flow and agent design |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Common issues and fixes |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to contribute |

---

## Built On

Code-Warden is built on [GoFrame](https://github.com/sevigo/goframe), a Go RAG framework that provides LLM chains, Qdrant integration, code-aware text splitting, multi-language AST parsing, and hybrid sparse/dense search.

## License

MIT — see [LICENSE](LICENSE).