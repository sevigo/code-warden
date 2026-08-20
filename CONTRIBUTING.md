# Contributing to Code-Warden

Bug fixes, features, docs, and tests are all welcome.

---

## Getting started

```sh
git clone https://github.com/sevigo/code-warden
cd code-warden
docker compose up -d db          # PostgreSQL
cp config.yaml.example config.yaml
make build
./bin/code-warden
```

For full setup including GitHub App configuration, see [docs/SETUP.md](docs/SETUP.md).

### Running tests and lint

```sh
make test        # Run all tests
make lint        # golangci-lint
```

Run a specific package:

```sh
go test -v ./internal/agent/review/...
go test -run TestTokenizer ./internal/llm/...
```

All tests and lint must pass before submitting a PR.

---

## Project structure

| Directory | What lives here |
|---|---|
| `cmd/` | Binary entry points (`server` and standalone `review`) |
| `internal/agent/review/` | Multi-angle agent-based review runner |
| `internal/agent/reviewtools/` | Workspace-bound read-only review tools |
| `internal/jobs/` | Job dispatcher and review worker |
| `internal/github/` | GitHub API client and webhook handling |
| `internal/storage/` | PostgreSQL persistence |
| `internal/core/` | Domain types and interfaces |
| `internal/llm/` | LLM client wrappers and prompt management |
| `internal/config/` | Configuration loading and defaults |
| `internal/wire/` | Dependency injection (Google Wire) |

---

## Common contribution patterns

### Adding a new review angle

1. Create `internal/llm/prompts/review_<angle>.prompt`
2. Add the prompt key constant to `internal/llm/prompt_manager.go`
3. Register the angle in `internal/agent/review/angles.go`

### Adding a new MCP tool

1. Create `internal/mcp/tools/<tool>.go` implementing the `Tool` interface:
   ```go
   type Tool interface {
       Name() string
       Description() string
       ParametersSchema() map[string]any
       Execute(ctx context.Context, args map[string]any) (any, error)
   }
   ```
2. Register it in `internal/mcp/server.go`
3. Add input validation (length limits, type assertions)

### Adding a new GitHub command

1. Parse the command in the webhook handler (`internal/server/handler/webhook.go`)
2. Add the event type to `internal/core/events.go`
3. Add the job handler in `internal/jobs/review.go`

### Adding a new prompt

1. Create `internal/llm/prompts/<name>.prompt`
2. Add the prompt key constant to `internal/llm/prompt_manager.go`
3. Use it via `promptMgr.Raw(llm.MyPromptKey)`

### Changing the database schema

1. Add migration SQL to `internal/db/migrations/`
2. Update the relevant `Store` interface in `internal/storage/database.go`
3. Update the PostgreSQL implementation
4. Update mock if the interface changed

---

## Dependency injection

Code-Warden uses [Google Wire](https://github.com/google/wire) for compile-time DI. If you add a new service:

1. Add a provider function (constructor) for your service
2. Register it in `internal/wire/wire.go`
3. Run `wire gen ./internal/wire/` to regenerate `wire_gen.go`

---

## Commit messages

```
<type>: <short summary>

<optional longer description>
```

Types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`

Examples:
```
feat: add security review angle
fix: resolve nil dereference in review runner
docs: update architecture overview
chore: upgrade goframe to v0.42.0
```

Keep the first line under 72 characters.

---

## Pull requests

- Open a PR against `main`
- Keep changes focused — one feature or fix per PR
- Include tests for new behaviour
- Update relevant documentation in `docs/`
- Ensure `make test` and `make lint` pass

For large changes, open an issue first to discuss the approach.
