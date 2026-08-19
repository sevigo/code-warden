# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Test Commands

```bash
make build          # Build server binary to ./bin/
make test           # Run all tests
make lint           # Run golangci-lint (auto-installs if missing)
make run            # Build and run the server
make ui-deps        # Install dashboard dependencies
make build-ui       # Type-check and build the UI
make quickstart     # Launch the guided Docker-based local stack
```

Run a single test:
```bash
go test -v ./internal/agent/review/... -run TestDeduplicate
go test -v ./internal/jobs/... -run TestValidation
```

Run tests with race detector:
```bash
go test -race ./...
```

## Architecture Overview

Code-Warden is an AI-powered code review assistant that reviews pull requests with an **agent-based** approach. It runs as a GitHub App and can use local models via Ollama or cloud models (Gemini, or any OpenAI-compatible endpoint).

### Review Flow
```
GitHub Webhook → Job Dispatcher → Review Worker
                                     │
                     ┌───────────────┼───────────────┐
                     │               │               │
                RepoManager     Agent Review    GitHub Client
                     │               │               │
                Git Operations  Multi-angle    Post Comments
                                agent passes
```

### Agent-Based Review (internal/agent/review)
The review runner clones the PR's checkout, then dispatches parallel agent passes that investigate the diff with grep + `read_file`:

1. **Bug** - logic errors, nil dereferences, error handling gaps, edge cases
2. **Security** - injection, auth bypass, path traversal, secrets
3. **Performance** - N+1 queries, allocations, unbounded growth
4. **Conventions** - naming, error wrapping, test coverage

Each pass uses read-only tools (grep, find, read_file, list_dir) in an isolated workspace. Findings are merged, deduplicated by `file:line`, and filtered to the diff hunks.

### Key Packages

| Package | Responsibility |
|---------|---------------|
| `internal/agent/review/` | Multi-angle agent-based review runner |
| `internal/agent/` | Orchestrator for `/implement` (plan → edit → review → publish) |
| `internal/jobs/` | Job dispatcher and review worker pool |
| `internal/repomanager/` | Git repository lifecycle (clone, sync, diff) |
| `internal/storage/` | PostgreSQL persistence |
| `internal/llm/` | LLM clients, prompts, and output parsing |
| `internal/github/` | GitHub API client and webhook handling |
| `internal/core/` | Domain types and interfaces |
| `internal/wire/` | Dependency injection with Google Wire |
| `internal/mcp/` | MCP server exposing tools to agents |
| `cmd/server/` | HTTP server entry point |

### Core Interfaces

```go
// Agent review runner - internal/agent/review/runner.go
type Runner struct { ... }
func (r *Runner) Run(ctx, params) (*Result, error)

// Store - internal/storage/database.go
```

## Dependency Injection

This project uses Google Wire for compile-time dependency injection. The wire definitions are in `internal/wire/wire.go`. Generated code is in `internal/wire/wire_gen.go`.

After adding new dependencies:
```bash
wire ./internal/wire/...
```

## GoFrame Integration

Code-Warden is built on the [GoFrame](https://github.com/sevigo/goframe) library. Key patterns:

```go
// Agent loop with tool registry
goframeagent.NewAgentLoop(model, registry, opts...)

// Parallel multi-angle dispatch with quorum
chains.NewMapReduceChain(mapFn, reduceFn,
    chains.WithMaxConcurrency(n),
    chains.WithMapTimeout(d),
    chains.WithQuorum(0.75),
)

// Read-only investigation tools (grep, find, read_file, list_dir)
```

## Testing Patterns

Tests use `stretchr/testify` and `go.uber.org/mock`:

```go
import (
    "testing"
    "github.com/stretchr/testify/assert"
    "go.uber.org/mock/gomock"
)

func TestSomething(t *testing.T) {
    ctrl := gomock.NewController(t)
    defer ctrl.Finish()

    mockStore := mocks.NewMockStore(ctrl)
    mockStore.EXPECT().Get(gomock.Any(), "key").Return(value, nil)

    // Use assert for assertions
    assert.Equal(t, expected, actual)
}
```

Mock files are in `mocks/` directory with naming convention `mock_<interface>.go`.

## LLM Provider Configuration

Three providers are supported:
- **Ollama** (default) - Local models, configured via `AI_OLLAMA_HOST`
- **Gemini** - Cloud models, configured via `AI_GEMINI_API_KEY`
- **OpenAI-compatible** - Any endpoint (OpenAI, Azure, OpenRouter, vLLM, LM Studio), configured via `AI_OPENAI_API_KEY` + `AI_OPENAI_BASE_URL`

The provider is selected via `AI_LLM_PROVIDER`.

## Configuration

### Application Level (Environment Variables)
Key variables:
- `AI_LLM_PROVIDER` - `ollama`, `gemini`, or `openai`
- `AI_GENERATOR_MODEL` - Model for review generation
- `AI_OLLAMA_HOST` - Ollama server URL
- `DATABASE_HOST` / `DATABASE_PASSWORD` - PostgreSQL connection
- `SERVER_MAX_WORKERS` - Concurrent review jobs

### Repository Level (`.code-warden.yml`)
```yaml
custom_instructions:
  - "Focus on security vulnerabilities"
exclude_dirs:
  - vendor
  - node_modules
exclude_exts:
  - .md
  - .txt
```

## Review Accuracy

The agent-based review enforces diff-boundary accuracy:
1. Findings are filtered to the changed lines in the diff
2. The agent reads the surrounding code before reporting an issue
3. Each angle must cite concrete evidence (`file:line`) for every finding
4. Duplicate findings across angles are deduplicated by `file:line`
