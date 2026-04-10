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

> **Pre-commit gate:** `make test && make lint` runs automatically before every
> `git commit` (enforced via a Claude Code PreToolUse hook in
> `.claude/settings.local.json`). The commit is blocked if either fails.

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
- **`cmd/cli`**: Administrative CLI (`warden-cli`) for update, prescan, review operations
- **`cmd/terminal`**: Terminal UI for local/debug interaction
- **`internal/core`**: Domain entities (`Review`, `Repository`, `PullRequest`, `JobQueue`, interfaces)
- **`internal/jobs`**: Background job execution (ReviewJob, Dispatcher)
- **`internal/review`**: Unified review execution flow shared by webhook, MCP tool, and CLI (`Executor` handles single-model and consensus modes)
- **`internal/llm`**: Prompt management, LLM client wrappers, tokenizer, JSON parser
- **`internal/rag`**: RAG service (`service.go`) + sub-packages:
  - `contextpkg/`: Context building — architecture summaries, HyDE, symbol search, impact analysis, TOC
  - `index/`: Qdrant indexing — file hashing, definitions extraction, TOC generation, filter rules
  - `review/`: Review generation — single-model, consensus (multi-model), rereview, artifact saving
  - `detect/`: Code reuse/duplication detection backed by VectorDB
  - `question/`: Free-form Q&A over indexed code
  - `metadata/`: Line-level document metadata
- **`internal/storage`**: PostgreSQL (`Store`) and Qdrant (`VectorStore`, `ScopedVectorStore`) data access
- **`internal/github`**: GitHub API client, webhook handling, diff hunks, status updates
- **`internal/repomanager`**: Git repository lifecycle (clone, sync, diff)
- **`internal/prescan`**: Repo preparation for CLI scanning (clone remote or open local, detect owner/repo)
- **`internal/mcp`**: Model Context Protocol server (SSE + JSON-RPC transports) exposing tools for AI agents; includes governance (allow/deny lists, rate limits)
- **`internal/globalmcp`**: Global MCP server running continuously alongside the main server; workspace-aware proxy/registry
- **`internal/agent`**: Orchestrates in-process AI coding agents (warden, native modes) — manages `Session` lifecycle, runs goframe AgentLoop, communicates via MCP; includes planner, file_tools, search_tools (grep/find), fuzzy_edit, context_crawler
- **`internal/gitutil`**: Git utilities (URL parsing, branch operations, clone/fetch/open)
- **`internal/wire`**: Google Wire dependency injection setup
- **`mocks/`**: Mock implementations for unit testing (generated with `go.uber.org/mock`)

### Data Flow (Review Process)

1. Webhook received → `WebhookHandler` validates and queues event
2. `JobDispatcher` assigns to worker → `ReviewJob.Run()` executes
3. `RepoManager.SyncRepo()` clones/fetches repo, calculates diff
4. `RAGService.UpdateRepoContext()` incrementally updates Qdrant vectors (via `rag/index`)
5. `review.Executor` calls `RAGService.GenerateReview()` — queries context (`rag/contextpkg`), renders prompt, calls LLM (`rag/review`)
6. Structured JSON review parsed and posted as GitHub PR comments

### MCP / Agent Flow

- `globalmcp.Server` runs on a dedicated port alongside the webhook server
- AI agents connect via SSE (`GET /sse`) or direct JSON-RPC (`POST /`)
- Available tools: `search_code`, `get_arch_context`, `get_symbol`, `get_structure`, `find_usages`, `get_callers`, `get_callees`, `review_code`, `push_branch`, `create_pull_request`, `list_issues`, `get_issue`, `grep`, `find`
- `create_pull_request` enforces that `review_code` was called and returned `APPROVE`/`COMMENT` within the last 30 minutes
- `internal/agent.Orchestrator` can spawn agent subprocesses and wire them to a per-session MCP server

### Key Patterns

- **Dependency Injection**: All services registered in `internal/wire/wire.go` using Google Wire
- **Error Handling**: Wrap errors with context: `fmt.Errorf("failed to <action>: %w", err)`
- **Context Propagation**: All I/O functions accept `context.Context` as first argument
- **Interface Naming**: Interfaces suffixed with `-er` (e.g., `Reviewer`, `ConfigLoader`)
- **Logging**: Uses `log/slog` throughout (via `internal/logger` wrapper); not zap

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

## Testing

- Unit tests use standard `go test` framework
- Mocks are in `mocks/` and generated with `go.uber.org/mock`; regenerate with `go generate ./...`
- Place tests in `*_test.go` files within the package they test
- Run a single test: `go test -v -run TestName ./internal/pkg/...`
- Integration tests skip when external services are unavailable

## Key Dependencies

- `github.com/google/wire`: Compile-time dependency injection
- `github.com/google/go-github`: GitHub API interactions
- `github.com/spf13/cobra`: CLI framework
- `github.com/jmoiron/sqlx`: PostgreSQL access
- `github.com/sevigo/goframe`: RAG framework (LLM, embeddings, vectorstores, agent registry/governance)
- `go.uber.org/mock`: Mock generation for unit tests (`mocks/` directory)

## GoFrame Integration

Code-Warden uses goframe v0.23.2+ with the following patterns:

### Chain Constructors (Return Errors)
```go
// All these constructors return errors as of goframe v0.23.2
chain, err := chains.NewLLMChain[T](llm, prompt, opts...)
qa, err := chains.NewRetrievalQA(retriever, llm, opts...)
retriever, err := vectorstores.NewDependencyRetriever(store)
defRetriever, err := vectorstores.NewDefinitionRetriever(store)
```

### Resource Cleanup
```go
// Always close VectorStore when done
defer vectorStore.Close()
```

### Context Propagation
```go
// Always pass context through to GoFrame operations
docs, err := store.SimilaritySearch(ctx, query, k)
sparseVec, err := sparse.GenerateSparseVector(ctx, text)
```

## Recent Improvements

### Agent Implement Flow (warden mode)
The in-process agent (`internal/agent/`) runs three sequential loops:

1. **Plan loop** (max 5 iterations, read-only MCP tools): produces a markdown
   implementation plan injected into the implement loop's system prompt.

2. **Implement loop** (max 50 iterations): all file tools + search tools + MCP tools except
   `push_branch`/`create_pull_request`. Terminates when `review_code` returns
   `APPROVE` or iterations are exhausted (draft PR is opened instead).

3. **Publish loop** (max 8 iterations, only if APPROVE): `push_branch` +
   `create_pull_request`.

**File tools** (`internal/agent/file_tools.go`):
- `read_file` — returns `{content, lines, path}`; supports `offset`/`limit`; when truncated, includes `total_lines`, `truncated`, and `hint` with next offset
- `write_file` — creates/overwrites; returns `{ok, path, bytes}`
- `edit_file` — two calling conventions:
  - Single: `{path, old_string, new_string}` (backwards-compatible)
  - Multi: `{path, edits:[{old_string, new_string}, ...]}` — atomic multi-replacement, applied in reverse-position order so earlier indices are not shifted; fuzzy-normalises the entire file if any match needs it
  - Returns `{ok, path, diff, fuzzy_match?}` — the `diff` field (unified diff, ≤4000 bytes) lets the LLM verify its changes without a follow-up `read_file`
  - Handles CRLF line endings and UTF-8 BOM transparently (normalizes before matching, restores on write)
- `list_dir` — returns directory entries with type and size

**Search tools** (`internal/agent/search_tools.go`):
- `grep` — searches file contents by regex/literal pattern; uses ripgrep with grep fallback; supports glob filter, case-insensitive mode, context lines; path is validated against workspace root
- `find` — lists files by glob pattern (including `**` for multi-level); pure Go `filepath.WalkDir`; skips `.git`/`node_modules`/`vendor`; returns workspace-relative paths

**Compaction** (`internal/agent/warden.go`):
- Triggers at 70% of a 128 K token ceiling
- Iterative: updates prior summary instead of summarising from scratch
- Appends `<read-files>` / `<modified-files>` XML blocks tracking cumulative file footprint across re-compactions
- Tail selection: walks backwards to the last human-role message so tool results are never orphaned from the AI turn that requested them

### Review Profile System
Code reviews now adapt intensity based on PR complexity (merged PR #237):
- **Quick profile**: Small/low-impact PRs — shorter context, fewer findings
- **Standard profile**: Moderate complexity — balanced review
- **Thorough profile**: Large/high-impact PRs — full context, detailed analysis
- High-risk paths (auth, crypto, payment, security) always force thorough review
- Profile stored in `internal/core/review_profile.go`, impact radius calculated in `internal/rag/contextpkg/builder.go`

### Background Job Context
Use server context for background jobs, not HTTP request context. The request context gets cancelled when the HTTP response is sent, causing jobs to fail. See `internal/jobs/dispatcher.go`.

### Sparse Query Validation
Small LLMs can generate malformed queries that crash sparse vector generation. Queries are validated before processing in `internal/rag/contextpkg/format.go`.

### Branch Protection
Main branch requires status checks (lint, test, build) but no PR approval. Direct commits to main are blocked.

## Common Tasks

### Adding a New Prompt
1. Create `internal/llm/prompts/my_prompt.prompt`
2. Add prompt key to `internal/llm/keys.go`
3. Use `promptMgr.Render(llm.MyPrompt, data)` in code

### Adding a New RAG Context Stage
1. Add method to `internal/rag/contextpkg/builder.go` (or a new file in `contextpkg/`)
2. Call from `buildContextConcurrently()` for parallel execution
3. Include result in context assembly via `BuildRelevantContext`

### Adding a New CLI Command
1. Create command in `cmd/cli/`
2. Register in `cmd/cli/root.go`
3. Add any new dependencies to `internal/wire/wire.go`

### Adding a New MCP Tool
1. Create tool struct in `internal/mcp/tools/` implementing the `Tool` interface (`Name`, `Description`, `ParametersSchema`, `Execute`)
2. Register in `internal/mcp/server.go` inside `registerTools()`
3. Inject any required dependencies through the struct fields

### Adding a New Agent Tool (grep/find-style)
1. Create tool struct in `internal/agent/` implementing the `mcp.Tool` interface
2. Add to `searchTools()` or `fileTools()` helper function
3. Tool is automatically registered in planner, implement, and native agent loops