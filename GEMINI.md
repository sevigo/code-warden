# Project Context for Code-Warden

This document provides structured, comprehensive context for the Language Model (LLM) to assist with development tasks for the Code-Warden project.

## 1. Project Identity and Goal

*   **Project Name:** Code-Warden
*   **Primary Goal:** To provide a GitHub App that performs contextual, repository-aware code reviews using a Large Language Model (LLM) when triggered by a PR comment (e.g., `/review`).
*   **Target Environment:** Primarily a long-running Go server that processes GitHub webhooks and executes background jobs.

## 2. Key Technology Stack and Dependencies

*   **Primary Language:** Go (Golang) 1.22+
*   **Secondary Language:** Python (for the `embeddings/` service)
*   **Major Go Dependencies:**
    *   `github.com/google/go-github`: For all GitHub API interactions.
    *   `github.com/spf13/cobra`: For building the `cli` and `terminal` applications.
    *   `github.com/jmoiron/sqlx` & `github.com/lib/pq`: For PostgreSQL database access.
    *   `github.com/uber-go/zap`: For structured logging.
    *   `github.com/google/wire`: Used for dependency injection.
*   **External Services:**
    *   **PostgreSQL:** Primary data store for repository, job, and review metadata.
    *   **Qdrant:** Vector database used to store and query code embeddings (RAG).
    *   **Ollama/Gemini API:** LLM providers for generating reviews.

## 3. Architectural Decisions

1.  **Dependency Injection:** The project uses **Google Wire** for compile-time dependency injection. All complex services, clients, and repositories are constructed in `internal/wire/wire.go`. New services must be added to the Wire provider sets.
2.  **Modular Design:** The codebase is split into feature-based packages (e.g., `llm`, `github`, `jobs`). This promotes high cohesion and low coupling.
3.  **Command-Query Responsibility Segregation (CQRS) Pattern:** Logic is separated into:
    *   **`jobs/`:** Handles the Command side (e.g., starting a review, updating status).
    *   **`db/` and `storage/`:** Handles the Query side (retrieving data).
4.  **RAG Mechanism:** Code indexing is performed in two stages:
    *   **Git Utility:** Identifies changed files.
    *   **Python Service (`embeddings/`):** Generates vector embeddings for the file content.
    *   **Go Service (`storage/`):** Stores the vectors in Qdrant for RAG at review time.

## 4. Code Style and Conventions

*   **Formatting:** All Go code **MUST** adhere to `go fmt` standards.
*   **Linting:** Follow standard Go idioms. Explicit error handling (`if err != nil`) is mandatory. **DO NOT** use `panic` in library or public functions.
*   **Naming:** Interfaces are named with the suffix `-er` (e.g., `Reviewer`, `ConfigLoader`). Structs are named after the domain entity (e.g., `Repository`, `Job`).
*   **Context:** All public functions that perform I/O or have a timeout must accept `context.Context` as the first argument.

## 5. Enhanced Project Structure

*   `cmd/`: Main application entry points.
    *   `server/`: The core GitHub App webhook server (runs the service).
    *   `cli/`: Command-line tool for administrative tasks (e.g., user management, manual indexing).
    *   `terminal/`: The Terminal User Interface (TUI) for local/debug interaction.
*   `internal/`: Private application and library code.
    *   `app/`: High-level application services that orchestrate core logic.
    *   `config/`: Logic for loading application configuration (uses environment variables and file loading).
    *   `core/`: **Core Domain Logic.** Defines key structs and interfaces such as `Review`, `Repository`, `PullRequest`, and `JobQueue`. This is the heart of the application's domain.
    *   `db/`: PostgreSQL database access, including repository structs and SQL helpers.
    *   `github/`: The GitHub client and webhook processing handlers.
    *   `jobs/`: Background job execution logic, including the implementation of the review process.
    *   `llm/`: Large Language Model integration. Contains the `PromptManager` responsible for selecting and rendering prompts (e.g., `internal/llm/prompts/`).
    *   `server/`: Web server setup, middleware, and HTTP handlers.
*   `embeddings/`: Python service for generating vector embeddings.

## 6. Database Schema Overview

The application uses PostgreSQL. Key tables include:

| Table Name | Purpose | Key Relationships |
| :--- | :--- | :--- |
| **repositories** | Stores metadata for all registered GitHub repositories. | One-to-many with `jobs`. |
| **jobs** | Stores the state and history of review requests (e.g., status, PR URL). | Foreign key to `repositories`. |
| **reviews** | Stores the final, generated LLM review comments. | Foreign key to `jobs`. |

## 7. Testing Strategy

*   **Framework:** Standard Go testing library (`go test`).
*   **Unit Tests:** Should be placed in `*_test.go` files within the package they test. Dependencies **MUST** be mocked using a mock generation tool (like **`mockery`** or similar) or by creating custom stubs/fakes that satisfy the interface.
*   **Integration Tests:** Reserved for testing interactions with external services (PostgreSQL, Qdrant). These should be isolated (e.g., run against a temporary Docker container) and use the `testing.T.Skip()` function if the necessary external services are not available in the test environment.
*   **Test Coverage:** Aim for high coverage on core domain logic (`internal/core/`) and service implementations (`internal/app/`, `internal/jobs/`).

## 8. Error Handling Examples

The project mandates explicit error wrapping for context propagation.

### Preferred Error Handling Pattern

```go
// internal/github/client.go

func (c *Client) GetPullRequest(ctx context.Context, repoID, prNum int) (*core.PullRequest, error) {
    // 1. Check for context cancellation/timeout early
    if err := ctx.Err(); err != nil {
        return nil, err
    }

    // ... GitHub API call ...
    pr, _, err := c.ghClient.PullRequests.Get(ctx, owner, repo, prNum)
    if err != nil {
        // 2. Wrap the error with context and use a clear, package-specific message
        return nil, fmt.Errorf("failed to fetch pull request %d from GitHub: %w", prNum, err)
    }

    // ...
}
```

**Rule:** Errors returned from lower layers (like `db/`, `github/`) must be wrapped using `fmt.Errorf("context message: %w", originalErr)` before being returned to the application layer (`app/`, `jobs/`).

## 9. Deployment Considerations

*   **Primary Artifact:** A single static binary (`code-warden-server`) is produced for the main application.
*   **Containerization:** The application is deployed via **Docker**. The `Dockerfile` uses a multi-stage build to produce a small, secure final image.
*   **Configuration:** All runtime configuration is provided exclusively through **environment variables** (parsed by `internal/config/`).
*   **Secrets Management:** Sensitive data (GitHub App private key, database credentials) are injected as secrets into the container environment (e.g., using Kubernetes Secrets or Docker Compose secrets).

## 10. Useful Makefile Targets

The `Makefile` simplifies common development and build tasks. Note that targets are chained, e.g., `make all` calls `make build`, which in turn calls `build-server` and `build-cli`.

| Target | Description |
| :--- | :--- |
| `make all` | Default goal. Calls `make build`. |
| `make build` | Builds both the `server` and `cli` binaries into the `./bin` directory. |
| `make build-server` | Compiles the main `code-warden` server binary. |
| `make build-cli` | Compiles the `warden-cli` administrative tool binary. |
| `make run` | Builds the server binary and then executes it from `./bin/code-warden`. |
| `make run-cli` | Executes the CLI tool directly using `go run ./cmd/cli`. |
| `make test` | Runs all Go unit tests (`go test -v ./...`) across the project. |
| `make lint` | Installs `golangci-lint` (if missing) and runs it on the entire codebase. |
| `make clean` | Removes the entire `./bin` directory containing all built binaries and tools. |

---
**LLM Directive on Missing Targets:**
*The provided Makefile does **not** include targets for building the `terminal` application binary, building a production Docker image (`make image-server`), or starting external dependencies via `docker-compose` (`make up`). If a task requires these, assume they must be implemented or performed manually, and **do not reference** them as existing Makefile targets.*
