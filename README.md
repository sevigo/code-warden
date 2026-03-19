# Code-Warden

[![Go Report Card](https://goreportcard.com/badge/github.com/sevigo/code-warden)](https://goreportcard.com/report/github.com/sevigo/code-warden)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**A self-hosted GitHub App that reviews pull requests with full understanding of your codebase — not just the diff.**

---

## Why Code-Warden Exists

Most AI code review tools read only the diff. They catch obvious mistakes, but they don't know that `UserService` is already implemented elsewhere, that your team agreed to avoid a certain pattern, or that the change you just made will break three other callers downstream.

Code-Warden was built to close that gap. Before generating a review, it retrieves architectural context, resolves type and function definitions, traces downstream impact through a dependency graph, and includes the PR's commit history as signal. The LLM receives a complete picture — not a fragment.

It runs entirely on your infrastructure. Your code never leaves.

---

## What Makes It Different

| Approach | Typical AI review tool | Code-Warden |
|---|---|---|
| Input to LLM | Diff only | Diff + codebase context |
| Symbol resolution | None | Exact Qdrant filter on definitions |
| Downstream impact | None | Dependency graph traversal |
| Architecture context | None | Pre-computed directory summaries |
| Search | Dense vectors | Hybrid (dense + code-aware sparse) |
| Review confidence | Single model | Consensus across multiple models |
| Privacy | Code sent to vendor | Fully self-hosted |
| Implementation | Manual | `/implement` — agent writes the code |

---

## How It Works

When a user comments `/review` on a pull request:

1. **Sync** — repo is cloned or incrementally updated; changed files are re-indexed into Qdrant
2. **Context** — five retrieval stages run in parallel:
   - *Architectural* — high-level module summaries for the directories touched
   - *HyDE* — hypothetical document embeddings to find semantically similar code
   - *Impact* — which other parts of the codebase call or import the changed symbols
   - *Description* — code related to the PR title, body, and commit messages
   - *Definitions* — exact type/function definitions for every symbol in the diff
3. **Review** — context + diff + custom instructions go to the LLM (or multiple models in consensus mode)
4. **Post** — severity-rated findings posted as inline GitHub comments with line-specific suggestions

The `/rereview` command runs a follow-up pass that compares the new diff against the original findings and reports what was fixed, what was missed, and what is new.

The `/implement` command goes further: an autonomous agent reads the issue, explores the codebase via MCP tools, writes the code, runs lint and tests, reviews its own work, and opens a pull request.

---

## Features

**Reviews**
- Context-aware: retrieves relevant code before the LLM sees the diff
- Consensus mode: query multiple models in parallel, synthesize into one review
- Re-review: validate whether previous findings were addressed
- Structured output: severity badges (🔴 critical · 🟠 warning · 🟡 suggestion) with inline comments

**Indexing**
- Incremental: only re-indexes files changed in the diff
- Hybrid search: dense embeddings + code-aware sparse vectors (camelCase/snake_case tokenization)
- Code-aware chunking: preserves function boundaries and propagates file-level metadata
- Multi-language AST parsing: extracts definitions, imports, and structure

**Agent (`/implement`)**
- Reads GitHub issue, plans, and implements changes in an isolated workspace
- Uses MCP tools: `search_code`, `get_symbol`, `get_arch_context`, `review_code`, `push_branch`
- Internal self-review loop before committing
- Only reviewed files are included in the PR

**Infrastructure**
- Self-hosted: Ollama (local) or cloud LLMs via proxy
- PostgreSQL for job history and review storage
- Qdrant for vector storage
- Configurable per repository via `.code-warden.yml`

---

## Quick Start

### Prerequisites

- Go 1.22+
- Docker & Docker Compose
- A GitHub App (see [GitHub App Setup](#github-app-setup))

### 1. Clone and configure

```sh
git clone https://github.com/sevigo/code-warden
cd code-warden
cp config.yaml.example config.yaml
# Edit config.yaml with your GitHub App credentials and model settings
```

### 2. Start services

```sh
docker-compose up -d                                    # Qdrant + PostgreSQL
docker-compose -f docker-compose.setup.yml up --build  # Pull Ollama models
```

### 3. Run

```sh
make build && ./bin/code-warden
# or for development:
go run ./cmd/server/main.go
```

### 4. Trigger a review

Comment `/review` on any open pull request in a repository where the GitHub App is installed. Code-Warden will clone the repo (first time), index it, and post findings.

---

## GitHub App Setup

1. Create a new GitHub App in your organization settings
2. Set the webhook URL to `https://your-host/webhook`
3. Request permissions: `Pull requests: Read & Write`, `Issues: Read & Write`, `Contents: Read`
4. Subscribe to events: `Pull request`, `Issue comment`, `Push`
5. Generate and download a private key
6. Install the app on the repositories you want reviewed

Set the credentials in `config.yaml`:

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
  sparse_vector_name: "code_sparse"
  context_token_budget: 16000      # RAG context budget (tokens)
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

Full configuration reference: [config.yaml.example](config.yaml.example)

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

## Where This Is Going

Code-Warden is actively developed. See [TODO.md](TODO.md) for the full roadmap. Highlights:

**GitHub interactions**
- `/review focus=security` — scoped reviews by category
- `/explain <symbol>` — look up a symbol in the index
- `/feedback wrong` on a review comment — marks it as a false positive; suppressed in future reviews
- Auto re-index on PR merge and push to default branch
- Auto-trigger review when review is requested on a PR

**Feedback loop**
- Store accept/ignore/wrong signals per finding
- Suppress recurring false positives automatically (written to `.code-warden.yml`)
- Track acceptance rate per finding category over time to tune future reviews

**Web UI**
- Repository status page (last indexed SHA, job history)
- Review explorer: browse past reviews, filter by severity and category
- Analytics dashboard: acceptance rate trends, most common findings, index freshness

**Planned commands**
- `/suggest` — generate a concrete code fix for a flagged issue
- `/ask <question>` — free-form RAG-powered question about the codebase
- `/why <snippet>` — explain why code was written this way (git blame + RAG)

---

## Documentation

| Document | Description |
|---|---|
| [docs/SETUP.md](docs/SETUP.md) | Step-by-step deployment and first-run guide |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Component relationships and system design |
| [docs/RAG_ARCHITECTURE.md](docs/RAG_ARCHITECTURE.md) | 6-stage RAG pipeline in detail |
| [docs/INDEXING.md](docs/INDEXING.md) | Chunk types, metadata schema, debugging retrieval |
| [docs/IMPLEMENT_ARCHITECTURE.md](docs/IMPLEMENT_ARCHITECTURE.md) | Agent orchestration and `/implement` flow |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Common issues and fixes |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to contribute |
| [TODO.md](TODO.md) | Full product roadmap |

---

## Built On

Code-Warden is built on [GoFrame](https://github.com/sevigo/goframe), a Go RAG framework providing LLM chains, Qdrant vector store integration, code-aware text splitting, multi-language AST parsing, and hybrid sparse/dense search.

---

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT — see [LICENSE](LICENSE).
