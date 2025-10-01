# Code-Warden: Technical Context Summary

## 1. Project Summary

Code-Warden is a self-hosted, AI-powered GitHub App for automated code reviews. It is written in Go and designed to run locally.

The core architecture uses a **Retrieval-Augmented Generation (RAG)** pipeline to provide context-aware feedback. It leverages a local or cloud-based LLM (Ollama/Gemini), a Qdrant vector store for code embeddings, and a PostgreSQL database for state management. The system is event-driven, reacting to GitHub webhooks.

## 2. Core Functionality & Architecture

The primary function is to perform an AI code review when a user comments `/review` on a pull request.

The system is composed of several key components:
-   **Web Server:** A Go server that listens for GitHub webhooks.
-   **Job Dispatcher:** An asynchronous worker pool that processes review requests in the background to avoid blocking webhooks.
-   **RAG Pipeline:** The core logic that converts code into vector embeddings, retrieves relevant context for a given code change, constructs a detailed prompt, and queries the LLM.
-   **Persistence Layer:** A PostgreSQL database tracks repository state and review history, while a Qdrant vector database stores code embeddings.
-   **Git & GitHub Clients:** Services dedicated to authenticating with GitHub, cloning/fetching repositories, and posting review feedback.
-   **CLI Tool:** An administrative CLI (`warden-cli`) for tasks like pre-loading and indexing large repositories.

The entire application is wired together using **Google Wire** for dependency injection (`internal/wire`).

## 3. Key Components & Responsibilities

-   **`App` (`internal/app/app.go`):** The central orchestrator that initializes and holds all major services (database, server, RAG service, etc.).
-   **`WebhookHandler` (`internal/server/handler/webhook.go`):** The entry point. It receives GitHub webhook events, validates them, and passes valid review commands to the `JobDispatcher`.
-   **`JobDispatcher` (`internal/jobs/dispatcher.go`):** Manages a queue and a pool of worker goroutines. It ensures that review jobs are processed asynchronously and concurrently up to a configured limit.
-   **`ReviewJob` (`internal/jobs/review.go`):** The main business logic for a single review. It coordinates all steps: setting up GitHub clients, syncing the repository, updating the vector store, generating the review, and posting the results.
-   **`RAGService` (`internal/llm/rag.go`):**
    -   `SetupRepoContext`: Performs a full scan and embedding of a repository.
    -   `UpdateRepoContext`: Performs an incremental update of the vector store based on a `git diff`.
    -   `GenerateReview`: Retrieves context from Qdrant, renders a prompt using `PromptManager`, calls the LLM, and parses the structured JSON output.
-   **`RepoManager` (`internal/repomanager/manager.go`):** Manages the lifecycle of Git repositories on disk. It handles initial cloning, fetching updates, and calculating diffs between commits to determine which files need re-indexing.
-   **`Store` (`internal/storage/database.go`):** The PostgreSQL database interface. It stores `repositories` (tracking `last_indexed_sha`) and `reviews` (history of review content).
-   **`VectorStore` (`internal/storage/vectorstore.go`):** The Qdrant vector store interface. It stores vector embeddings of code chunks and performs similarity searches.
-   **`PromptManager` (`internal/llm/prompt_manager.go`):** Loads and manages prompt templates from embedded files, allowing for different prompts based on the LLM provider.

## 4. Primary Data Flow: The `/review` Process

1.  A user comments `/review` on a PR.
2.  The `WebhookHandler` receives the `issue_comment` event, validates it, and creates a `core.GitHubEvent`.
3.  The event is passed to the `JobDispatcher`, which places it in a queue for a worker.
4.  A worker picks up the job and executes `ReviewJob.Run()`.
5.  The `ReviewJob` authenticates with GitHub using the installation ID to create a `github.Client` and a `StatusUpdater`. It posts an "in-progress" check run status.
6.  It calls `RepoManager.SyncRepo()` with the event's `head_sha`.
    -   The `RepoManager` checks the `Store` (PostgreSQL) for an existing repository record.
    -   **If new:** It clones the repository to a persistent local path.
    -   **If existing:** It opens the local repository, fetches the latest changes from origin, and calculates a `git diff` between the `last_indexed_sha` from the database and the new `head_sha`.
7.  The `ReviewJob` receives an `UpdateResult` from the `RepoManager` containing lists of files to add, update, or delete.
8.  It calls `RAGService.UpdateRepoContext()` to update the Qdrant vector store. This is an incremental update unless it's an initial clone.
9.  After the vector store is updated, the `ReviewJob` updates the `last_indexed_sha` in the PostgreSQL `Store`.
10. The `ReviewJob` calls `RAGService.GenerateReview()`. This service gets the PR diff, queries Qdrant for relevant context, renders the `code_review` prompt, and calls the configured LLM.
11. **Crucially, the LLM is prompted to return a structured JSON object (`core.StructuredReview`)** containing a summary and a list of `Suggestion` objects, each with a file path and line number.
12. The `ReviewJob` receives this structured data. It calls `StatusUpdater.PostStructuredReview()` to post the summary as a main comment and each `Suggestion` as a line-specific comment in the PR's "Files Changed" view.
13. Finally, the `ReviewJob` updates the GitHub check run to "completed" with a "success" status.

## 5. Configuration

-   **Application Level (`.env`):** Manages secrets, connection strings (DB, Qdrant, Ollama), and GitHub App credentials. Handled by `internal/config/config.go`.
-   **Repository Level (`.code-warden.yml`):** Allows users to customize behavior on a per-repository basis. Supports `custom_instructions` for the LLM prompt and `exclude_dirs`/`exclude_exts` for the indexing process.

## 6. Development Status & Roadmap (from `TODO.md`)

-   **Current State:** The system has recently implemented two critical features: **structured JSON-based reviews** for line-specific comments and **intelligent incremental RAG context caching** based on `git diff` to make subsequent reviews much faster.
-   **Immediate Priority:** Re-implement the `/rereview` command to work with the new structured review format. This involves creating a new prompt that compares a new diff against the original JSON suggestions.
-   **Next Steps:** Develop a simple web UI for status monitoring and repository management.
-   **Future Goals:** Implement GitHub's "Suggested Changes" feature and add resource garbage collection for old repositories.