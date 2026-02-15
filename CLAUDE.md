# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Code-Warden is a self-hosted GitHub App that performs contextual code reviews using LLMs (Ollama or Gemini). It uses a RAG (Retrieval-Augmented Generation) pipeline with Qdrant vector store to provide repository-aware feedback when triggered by `/review` or `/rereview` commands on pull requests.

## Build and Test Commands

```bash
# Build both server and CLI binaries
make build

# Build individual binaries
make build-server   # Creates ./bin/code-warden
make build-cli      # Creates ./bin/warden-cli

# Run the server
make run            # Build and run server
go run ./cmd/server/main.go

# Run tests
make test           # Runs go test -v ./...
go test -v ./...    # Direct test execution
go test -v ./internal/llm/...  # Run tests for specific package

# Lint code
make lint           # Installs golangci-lint if needed and runs it

# Clean build artifacts
make clean
```

## Development Environment

Start required services (PostgreSQL and Qdrant):
```bash
docker-compose up -d
```

For Ollama models (if using ollama provider):
```bash
docker-compose -f docker-compose.setup.yml up --build --remove-orphans
```

## Architecture

### Core Components

- **`cmd/server`**: GitHub App webhook server entry point
- **`cmd/cli`**: Administrative CLI (`warden-cli`) for preload, scan, review operations
- **`cmd/terminal`**: Terminal UI for local/debug interaction
- **`internal/core`**: Domain entities (`Review`, `Repository`, `PullRequest`, `JobQueue`, interfaces)
- **`internal/jobs`**: Background job execution (ReviewJob, Dispatcher)
- **`internal/llm`**: RAG pipeline, prompt management, LLM integration
- **`internal/storage`**: PostgreSQL (Store) and Qdrant (VectorStore) data access
- **`internal/github`**: GitHub API client, webhook handling, status updates
- **`internal/repomanager`**: Git repository lifecycle (clone, sync, diff)
- **`internal/wire`**: Google Wire dependency injection setup

### Data Flow (Review Process)

1. Webhook received → `WebhookHandler` validates and queues event
2. `JobDispatcher` assigns to worker → `ReviewJob.Run()` executes
3. `RepoManager.SyncRepo()` clones/fetches repo, calculates diff
4. `RAGService.UpdateRepoContext()` incrementally updates Qdrant vectors
5. `RAGService.GenerateReview()` queries context, renders prompt, calls LLM
6. Structured JSON review parsed and posted as GitHub PR comments

### Key Patterns

- **Dependency Injection**: All services registered in `internal/wire/wire.go` using Google Wire
- **Error Handling**: Wrap errors with context: `fmt.Errorf("failed to <action>: %w", err)`
- **Context Propagation**: All I/O functions accept `context.Context` as first argument
- **Interface Naming**: Interfaces suffixed with `-er` (e.g., `Reviewer`, `ConfigLoader`)

## Configuration

Configuration via `config.yaml` or environment variables (env vars take precedence, use `SECTION_KEY` format like `AI_LLM_PROVIDER`):

Key settings:
- `ai.llm_provider`: "ollama" or "gemini"
- `ai.generator_model`: Model for reviews (e.g., "gemma3:latest", "gemini-1.5-flash")
- `ai.embedder_model`: Model for embeddings (e.g., "nomic-embed-text")
- `github.app_id`, `github.webhook_secret`, `github.private_key_path`: GitHub App credentials
- `database.*`: PostgreSQL connection settings
- `storage.qdrant_host`: Qdrant endpoint

Repository-specific customization via `.code-warden.yml` in repo root:
- `custom_instructions`: List of guidance strings for the LLM
- `exclude_dirs`, `exclude_exts`: Files to skip during indexing

## Python Embeddings Service

Located in `embeddings/main.py` - a FastAPI service for generating code embeddings using transformers. Used as an alternative embedding backend for high-dimensional models.

## Testing

- Unit tests use standard `go test` framework
- Mock interfaces using `go.uber.org/mock` or custom stubs
- Place tests in `*_test.go` files within the package they test
- Integration tests should skip if external services unavailable

## Key Dependencies

- `github.com/google/wire`: Compile-time dependency injection
- `github.com/google/go-github`: GitHub API interactions
- `github.com/spf13/cobra`: CLI framework
- `github.com/jmoiron/sqlx`: PostgreSQL access
- `github.com/sevigo/goframe`: RAG framework (LLM, embeddings, vectorstores)
- `go.uber.org/zap`: Structured logging (via internal logger wrapper)