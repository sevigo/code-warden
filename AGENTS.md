# Repository Guidelines

## Project Structure & Module Organization

Code-Warden has two Go entry points under `cmd/`: `server` (the GitHub App + web UI) and `review` (a standalone CLI that runs the agent-based review locally or against a public PR, without GitHub integration). Application code lives in `internal/`: `agent/` runs implementation and review workflows (including `agent/review/` for the multi-angle agent-based review and `reviewcli/` + `reviewcli/render/` for the standalone CLI), `github/` integrates with GitHub, `mcp/` exposes tools, and `storage/` and `db/migrations/` manage persistence. Generated mocks belong in `mocks/`. The React/TypeScript dashboard is in `ui/src/`; documentation and helper scripts live in `docs/` and `scripts/`.

## Build, Test, and Development Commands

- `make build` builds the Go binary into `bin/`.
- `make run` builds and starts the server.
- `make test` runs `go test -v ./...`.
- `make lint` runs the pinned golangci-lint configuration from `.golangci.yml`.
- `make ui-deps` installs dashboard dependencies; `make dev-ui` starts Vite; `make build-ui` type-checks and builds the UI.
- `make quickstart` launches the guided Docker-based local stack.

## Coding Style & Naming Conventions

Format Go files with `goimports` (or `gofmt`); use tabs as emitted by these tools. Exported identifiers use `PascalCase`, internal identifiers use `camelCase`, and package names are short lowercase words. Keep packages narrow and return errors with useful context. TypeScript uses two-space indentation, `PascalCase` for React components, and `camelCase` for hooks and helpers. Do not hand-edit `internal/wire/wire_gen.go` or `mocks/mock_*.go`; regenerate them with Wire or `go generate`.

## Testing Guidelines

Place Go tests beside implementations as `*_test.go`, with functions named `TestXxx`; prefer table-driven cases for varied inputs. Tests use Go's `testing` package plus `testify/assert` and `testify/require`. Run targeted tests with `go test -v ./internal/agent/review/...`, then run `make test` and `make lint`. CI records coverage but sets no numeric threshold; new behavior and fixes should include focused tests.

## Commit & Pull Request Guidelines

History follows Conventional Commit-style subjects such as `feat(agent): ...`, `fix: ...`, and `docs: ...`. Use an imperative summary under 72 characters; common types are `feat`, `fix`, `docs`, `test`, `refactor`, and `chore`. Open PRs against `main`, keep changes focused, explain intent and verification, link issues, and update affected documentation. Include screenshots for dashboard changes and ensure build, tests, and lint pass.

## Security & Configuration

Do not commit `.env`, GitHub tokens, private keys, or populated local configuration. Use `.env.example` and `config.yaml.example` as templates, and keep application keys outside the repository.
