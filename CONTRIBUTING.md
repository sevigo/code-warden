# Contributing to Code-Warden

Contributions are welcome — bug fixes, new features, documentation improvements, and test coverage.

---

## Getting started

### Local stack

```sh
# Clone
git clone https://github.com/sevigo/code-warden
cd code-warden

# Start Qdrant and PostgreSQL
docker-compose up -d

# Copy and edit config
cp config.yaml.example config.yaml

# Build
make build

# Run
./bin/code-warden
```

For a full setup including GitHub App configuration, see [docs/SETUP.md](docs/SETUP.md).

### Running tests and lint

```sh
make test        # Run all tests
make test-race   # Run with race detector (catches concurrency bugs)
make lint        # Run golangci-lint
```

Run a specific package:

```sh
go test -v ./internal/rag/...
go test -run TestTokenizer ./internal/...
```

All tests must pass and lint must return zero issues before submitting a PR.

---

## Project structure

| Directory | What lives here |
|---|---|
| `cmd/` | Binary entry points (server, CLI, terminal) |
| `internal/rag/` | RAG pipeline — context building, indexing, review generation |
| `internal/jobs/` | Job dispatcher and review worker |
| `internal/github/` | GitHub API client and webhook handling |
| `internal/agent/` | Agent orchestration for `/implement` |
| `internal/mcp/` | MCP server and tool implementations |
| `internal/storage/` | PostgreSQL and Qdrant abstractions |
| `internal/core/` | Domain types and interfaces |
| `internal/llm/` | LLM client wrappers and prompt management |
| `internal/config/` | Configuration loading and defaults |
| `internal/wire/` | Dependency injection (Google Wire) |

---

## Common contribution patterns

### Adding a new RAG context stage

1. Create `internal/rag/contextpkg/<stage>.go`
2. Implement your retrieval logic returning `[]schema.Document`
3. Add it to the parallel stage runner in `internal/rag/contextpkg/builder.go`
4. Add a corresponding section in [docs/RAG_ARCHITECTURE.md](docs/RAG_ARCHITECTURE.md)
5. Write tests in `internal/rag/contextpkg/<stage>_test.go`

### Adding a new MCP tool

1. Create `internal/mcp/tools/<tool>.go` implementing the `Tool` interface:
   ```go
   type Tool interface {
       Name() string
       Description() string
       InputSchema() map[string]any
       Execute(ctx context.Context, args map[string]any) (any, error)
   }
   ```
2. Register it in `internal/mcp/server.go`
3. Add input validation (length limits, type assertions)
4. Document it in [docs/IMPLEMENT_ARCHITECTURE.md](docs/IMPLEMENT_ARCHITECTURE.md)

### Adding a new GitHub command

1. Parse the new command in the webhook handler (`internal/github/`)
2. Add the event type to `internal/core/events.go`
3. Add the job handler in `internal/jobs/review.go`
4. Add the command to the commands table in [TODO.md](TODO.md) if it's not yet implemented

### Adding a new prompt

1. Create `internal/llm/prompts/<name>.prompt`
2. Add the prompt key constant to `internal/llm/keys.go`
3. Use it via `promptMgr.Render(llm.MyPromptKey, data)`

### Changing the database schema

1. Add migration SQL to `internal/db/migrations/`
2. Update the relevant `Store` interface in `internal/storage/store.go`
3. Update the PostgreSQL implementation
4. Update mock if the interface changed

---

## Dependency injection

Code-Warden uses [Google Wire](https://github.com/google/wire) for compile-time dependency injection. If you add a new service:

1. Add a provider function (constructor) for your service
2. Register it in `internal/wire/wire.go`
3. Run `wire gen ./internal/wire/` to regenerate `wire_gen.go`

---

## Commit messages

Use the conventional commits format:

```
<type>: <short summary>

<optional longer description>
```

Types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`

Examples:
```
feat: add /explain command for symbol lookup via RAG
fix: resolve stale LSP cache causing false positives in client.go
docs: update RAG_ARCHITECTURE with test coverage stage
chore: upgrade goframe to v0.36.0
```

Keep the first line under 72 characters.

---

## Pull requests

- Open a PR against `main`
- Keep changes focused — one feature or fix per PR
- Include tests for new behaviour
- Update relevant documentation in `docs/`
- Ensure `make test` and `make lint` pass

For large changes, open an issue first to discuss the approach before investing significant time.
