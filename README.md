# Code-Warden

[![Go Reference](https://pkg.go.dev/badge/github.com/sevigo/code-warden.svg)](https://pkg.go.dev/github.com/sevigo/code-warden)
[![Go Report Card](https://goreportcard.com/badge/github.com/sevigo/code-warden)](https://goreportcard.com/report/github.com/sevigo/code-warden)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

An AI-powered code review assistant that runs locally with hallucination-resistant RAG.

Code-Warden is a GitHub App that uses Large Language Models (LLMs) to perform intelligent code reviews. It leverages Retrieval-Augmented Generation (RAG) to understand your entire codebase, providing context-aware feedback that goes beyond simple diff analysis.

## Overview

Code-Warden is designed for development teams who want AI-assisted code reviews without sending their code to external services. It runs entirely on your infrastructure, using local models via [Ollama](https://ollama.com/) or cloud providers like Google [Gemini](https://deepmind.google/technologies/gemini/).

## Features

### Core Features

-   **Flexible LLM Providers**: Supports local models via Ollama and cloud models like Google Gemini
-   **Context-Aware Reviews**: Uses RAG to understand the entire codebase, not just the diff
-   **Incremental Indexing**: Smart updates based on `git diff`—only re-indexes changed files
-   **Consensus Reviews**: Multi-model reviews with automatic synthesis for higher confidence
-   **Repository Configuration**: Customize behavior via `.code-warden.yml` in your repository

### RAG Pipeline

-   **5-Stage Context Building**: Architectural, HyDE, Impact, Description, and Definitions context
-   **Hybrid Search**: Dense + sparse vectors for semantic and exact matching
-   **Hallucination Prevention**: Empty context detection with warning injection
-   **Dependency Graph Traversal**: "Who uses this code?" impact analysis
-   **Symbol Resolution**: Extracts and resolves type/function definitions from diffs

### Review Features

-   **Structured Output**: Reviews with severity badges (🔴, 🟠, 🟡) and categories
-   **Inline Comments**: Line-specific feedback directly in the PR
-   **Re-Review**: Follow-up reviews that validate previous suggestions
-   **Custom Instructions**: Per-PR guidance like `/review focus on security`

## Architecture

Code-Warden follows an event-driven architecture with a multi-stage RAG pipeline:

```
[GitHub Webhook] → [Job Dispatcher] → [Review Worker]
                                           │
                     ┌─────────────────────┼─────────────────────┐
                     │                     │                     │
               [Repo Manager]      [RAG Service]         [GitHub Client]
                     │                     │                     │
               [Git Operations]    [Vector Store]         [Post Comments]
                     │                     │
               [File Sync]         [5-Stage Context]
                                         │
                     ┌───────────────────┼───────────────────┐
                     │                   │                   │
              [Architecture]      [Impact Analysis]    [Definitions]
               [Context]           [HyDE Context]       [Context]
```

### RAG Pipeline Stages

| Stage | Purpose | Source |
|-------|---------|--------|
| **Architectural** | High-level module understanding | Pre-computed directory summaries |
| **HyDE** | Semantic code discovery | Hypothetical document embeddings |
| **Impact** | Find affected downstream code | Dependency graph traversal |
| **Description** | Code related to PR intent | MultiQuery retrieval |
| **Definitions** | Type/function resolution | Symbol extraction + exact lookup |

### Data Flow

```
1. User comments /review on PR
2. Webhook → Job Dispatcher → Worker Pool
3. RepoManager syncs repo (clone or incremental update)
4. RAG Service updates vector store with changed files
5. 5-stage parallel context building
6. Context assembly with deduplication & validation
7. LLM generates structured review
8. Post review as GitHub comments
```

### Package Layout

| Directory | Purpose |
|-----------|---------|
| `internal/rag/` | Multi-stage RAG pipeline implementation |
| `internal/jobs/` | Job dispatcher and review worker pool |
| `internal/repomanager/` | Git repository lifecycle management |
| `internal/storage/` | PostgreSQL and Qdrant abstractions |
| `internal/llm/` | LLM client, prompts, and output parsing |
| `internal/github/` | GitHub API client and webhook handling |
| `internal/core/` | Domain types and interfaces |
| `cmd/` | Server and CLI entry points |

## Documentation

| Document | Description |
|----------|-------------|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | High-level architecture, component relationships, separation of concerns |
| [docs/RAG_ARCHITECTURE.md](docs/RAG_ARCHITECTURE.md) | Detailed RAG pipeline documentation (5-stage context building) |
| [docs/IMPLEMENT_ARCHITECTURE.md](docs/IMPLEMENT_ARCHITECTURE.md) | `/implement` command architecture (agent orchestration) |
| [docs/opencode-config.md](docs/opencode-config.md) | OpenCode agent configuration |

## Quick Start

### Prerequisites

- Go 1.22+
- Docker & Docker Compose
- GitHub App credentials

### 1. Configure Environment

```sh
cp .env.example .env
# Edit .env with your credentials
```

Key settings:
```env
GITHUB_APP_ID=your_app_id
GITHUB_PRIVATE_KEY_PATH=keys/app.private-key.pem
LLM_PROVIDER=ollama  # or gemini
GENERATOR_MODEL_NAME=gemma3:latest
EMBEDDER_MODEL_NAME=nomic-embed-text
```

### 2. Start Services

```sh
docker-compose up -d
docker-compose -f docker-compose.setup.yml up --build  # Pull models
```

### 3. Run the Server

```sh
go run ./cmd/server/main.go
# Or build and run:
make build && ./bin/code-warden
```

## How It Works

### Review Generation

1. **Trigger**: Comment `/review` on a pull request
2. **Sync**: Clone/update repository, calculate `git diff`
3. **Index**: Update vector store with changed files
4. **Context**: Build 5-stage RAG context in parallel
5. **Generate**: LLM produces structured review with line-specific suggestions
6. **Post**: Summary comment + inline code suggestions

### Consensus Review

When `comparison_models` are configured, Code-Warden:

1. Queries all models in parallel
2. Each model reviews independently
3. Synthesizes findings into unified review
4. Adds consensus disclaimer

### Re-Review

The `/rereview` command validates previous suggestions:

1. Fetches original review from database
2. Compares new diff against original suggestions
3. Reports which issues were fixed, missed, or new

## CLI Commands

### Update Repository

```sh
# Incremental update (fast, git-diff based)
./bin/warden-cli update /path/to/repo

# Full scan with resume support
./bin/warden-cli prescan /path/to/repo
```

### Review Pull Request

```sh
export CW_GITHUB_TOKEN="ghp_xxx"
./bin/warden-cli review https://github.com/owner/repo/pull/123
./bin/warden-cli review --verbose https://github.com/owner/repo/pull/123
```

## Configuration

### Application Level (`.env`)

| Variable | Description | Default |
|----------|-------------|---------|
| `LLM_PROVIDER` | LLM provider (`ollama` or `gemini`) | `ollama` |
| `GENERATOR_MODEL_NAME` | Model for review generation | `gemma3:latest` |
| `FAST_MODEL_NAME` | Model for quick tasks | `gemma3:latest` |
| `EMBEDDER_MODEL_NAME` | Model for embeddings | `nomic-embed-text` |
| `OLLAMA_HOST` | Ollama server URL | `http://localhost:11434` |
| `QDRANT_HOST` | Qdrant server URL | `localhost:6334` |
| `MAX_WORKERS` | Concurrent review jobs | `5` |
| `ENABLE_HYDE` | Enable HyDE context | `true` |
| `ENABLE_RERANKING` | Enable LLM reranking | `true` |

### Repository Level (`.code-warden.yml`)

```yaml
# Place in repository root
custom_instructions:
  - "Focus on security vulnerabilities"
  - "Check for proper error handling"

exclude_dirs:
  - vendor
  - node_modules
  - dist

exclude_exts:
  - .md
  - .txt
```

## Hallucination Prevention

Code-Warden implements multiple safeguards against LLM hallucinations:

1. **Empty Context Detection**: Warns when no relevant context is found
2. **Snippet Validation**: Fast LLM validates retrieved snippets
3. **Document Deduplication**: Parent-aware keys prevent duplicates
4. **Hybrid Search**: Dense + sparse vectors improve recall
5. **Symbol Resolution**: Exact-match filters for definitions

## GoFrame Integration

Code-Warden is built on [GoFrame](https://github.com/sevigo/goframe), utilizing:

| Pattern | Usage |
|---------|-------|
| `chains.LLMChain[T]` | Typed LLM calls with output parsing |
| `chains.RetrievalQA` | Question answering |
| `chains.MapReduceChain` | Consensus review generation |
| `vectorstores.VectorStore` | Qdrant operations |
| `textsplitter.TextSplitter` | Code-aware chunking |
| `documentloaders.GitLoader` | Streaming repository ingestion |
| `parsers.ParserRegistry` | Multi-language AST parsing |

## API Reference

Full API documentation is available at [pkg.go.dev](https://pkg.go.dev/github.com/sevigo/code-warden).

## Development

### Running Tests

```sh
make test        # Run all tests
make test-race   # Run with race detector
make lint        # Run linters
```

### Project Structure

```
code-warden/
├── cmd/
│   ├── cli/          # CLI entry point
│   └── server/       # Server entry point
├── internal/
│   ├── app/          # Application bootstrap
│   ├── config/       # Configuration
│   ├── core/         # Domain types
│   ├── db/           # Database connection
│   ├── github/       # GitHub API client
│   ├── gitutil/      # Git operations
│   ├── jobs/         # Job dispatcher
│   ├── llm/          # LLM and prompts
│   ├── logger/       # Logging setup
│   ├── prescan/      # Pre-scanning logic
│   ├── rag/          # RAG pipeline
│   ├── repomanager/  # Repository management
│   ├── server/       # HTTP server
│   ├── storage/      # Data persistence
│   └── wire/         # Dependency injection
├── embeddings/       # FastAPI embedding service
└── examples/         # Example configurations
```

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.