# Project Instructions: Code-Warden

You are an expert Go developer working on `Code-Warden`, an AI-powered code review assistant.

## Project Goal
To provide a GitHub App that performs contextual, repository-aware code reviews using an LLM (Ollama or Gemini) when triggered by a PR comment.

## Architecture
- **Language**: Go 1.22+ (Server), Python (Embeddings service).
- **Core Components**:
  - `cmd/server`: GitHub App webhook server.
  - `cmd/cli`: Admin CLI.
  - `internal/core`: Domain entities (`Review`, `Repository`).
  - `internal/jobs`: CQRS Command handlers (background jobs).
  - `internal/db`: PostgreSQL data access.
  - `internal/storage`: Qdrant vector store access.
  - `embeddings/`: Python service for generating vectors.
- **Dependencies**: Google Wire (DI), SQLx (Postgres), Zap (Logging).

## Development Guidelines
1.  **Dependency Injection**: Use `google/wire`. Register new services in `internal/wire/wire.go`.
2.  **Error Handling**: Wrap errors with context: `fmt.Errorf("failed to <action>: %w", err)`.
3.  **Testing**:
    - Unit tests in `*_test.go`.
    - Mock interfaces using `mockery` or `gomock`.
    - Use `go test ./...`.
4.  **Database**:
    - Use `sqlx` for interactions.
    - Migrations are handled via `golang-migrate`.
5.  **Context**: All I/O functions must accept `context.Context`.

## Key Workflows
- **Indexing**: `GitUtility` -> `embeddings` (Python) -> `storage` (Qdrant).
- **Review**: Webhook -> `jobs.ReviewJob` -> `LLM` -> GitHub Comment.

## Tech Stack
-   **Go**: 1.22+
-   **Vector DB**: Qdrant
-   **DB**: PostgreSQL
-   **LLM**: Ollama / Gemini

## Important Files
- `GEMINI.md`: Detailed project context.
- `.code-warden.yml`: Repo-specific config.
