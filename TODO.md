# TODO

This document outlines the development roadmap for Code-Warden. It tracks completed features, immediate priorities, and future ideas.

## ✅ Recently Completed

-   **1. Structured, Line-Specific Reviews:** The review process has been fundamentally upgraded.
    -   **Resolution:** The system now prompts the LLM for a structured JSON output. This JSON is parsed into `Suggestions` which are then posted as line-specific comments on the pull request using the GitHub Review API.
    -   **Benefit:** Feedback is now contextual, actionable, and appears exactly where it's relevant.

-   **2. Intelligent RAG Context Caching:** This was the most critical step to make the tool practical.
    -   **Resolution:** The system tracks repository state in PostgreSQL (`last_indexed_sha`). Subsequent reviews perform a `git diff` to incrementally update the vector store.
    -   **Benefit:** Reduces subsequent review time from minutes to seconds.

-   **3. Repository Configuration (`.code-warden.yml`):** Users can customize behavior.
    -   **Resolution:** A `.code-warden.yml` file allows for `custom_instructions`, `exclude_dirs`, and `exclude_exts`.
    -   **Benefit:** Makes the tool adaptable to team-specific needs.

-   **4. Re-implemented the `/rereview` Command:**
    -   **Resolution:** The `/rereview` command validates previous suggestions against new changes.
    -   **Benefit:** Developers can request fresh analysis after pushing fixes.

-   **5. Enhanced GitHub Comment Formatting:**
    -   **Resolution:** Inline comments feature severity badges (🔴, 🟠, 🟡, 🟢) and categories. Comments are tied to specific commit SHAs.
    -   **Benefit:** Improved readability and professional look.

-   **6. GoFrame v0.23.2 Compatibility (Feb 2025):**
    -   **Resolution:** Updated all GoFrame API calls to handle new error returns from constructors (`NewLLMChain`, `NewRetrievalQA`, `NewDependencyRetriever`, `NewDefinitionRetriever`).
    -   **Benefit:** Maintains compatibility with latest GoFrame features.

-   **7. Resource Leak Fixes (Feb 2025):**
    -   **Resolution:** Added `Close()` method to `VectorStore` interface, properly closing gRPC connections in `App.Stop()`.
    -   **Benefit:** Prevents resource leaks on application shutdown.

-   **8. Context Propagation Improvements (Feb 2025):**
    -   **Resolution:** Fixed `context.Background()` usage throughout RAG pipeline. Context now properly propagates through `getOrCreateLLM`, `ProcessFile`, `SplitDocuments`, and `GenerateSparseVector`.
    -   **Benefit:** Proper cancellation support for long-running operations.

## 🚀 Next Up: Immediate Priorities

### 1. **Create a Simple Web UI for Status & Onboarding**

Provide a user-friendly way to see what the app is doing and what repositories are managed.

-   **TODO:**
    1.  Add frontend routes in `internal/server/router.go`
    2.  Build a status page listing all repositories with last indexed SHA
    3.  Show job history with status and PR links
-   **Benefit:** Improves transparency and user experience.

### 2. **Add Godoc Documentation**

Improve package-level documentation for better discoverability.

-   **TODO:**
    1.  Add godoc comments to `internal/storage/` interfaces
    2.  Document `internal/rag/` service methods
    3.  Document `internal/jobs/` dispatcher and worker
    4.  Add package-level documentation
-   **Benefit:** Better developer experience and API discoverability.

### 3. **Performance Optimizations**

Address memory and performance issues identified in code review.

-   **TODO:**
    1.  Pre-allocate `allDocs` slice in `rag_index.go`
    2.  Add cleanup for unbounded maps in `vectorstore.go` and `repomanager/manager.go`
    3.  Add metrics/alerting for bounded job queue
-   **Benefit:** More stable long-running operations.

## 💡 Future Enhancements & Ideas

### 4. **Implement GitHub "Suggested Changes"**

Allow the AI to suggest concrete code changes that developers can accept with a single click.

-   **TODO:**
    1.  Add `code_suggestion` field to `core.Suggestion`
    2.  Use GitHub Review API suggestion format (````suggestion` code fence)
    3.  Update prompts to request code suggestions
-   **Benefit:** Reduces friction for accepting AI feedback.

### 5. **Implement Resource Lifecycle Management**

Ensure long-term stability with garbage collection.

-   **TODO:**
    1.  Create a "Janitor" background service
    2.  TTL-based cleanup for old repositories (Qdrant collections, disk files, DB records)
    3.  Handle GitHub App uninstallation events
-   **Benefit:** Prevents resource leaks and controls operational costs.

### 6. **Advanced RAG Enhancements**

Further improve retrieval quality and reduce hallucinations.

-   **TODO:**
    1.  Integrate `chains.ValidatingRetrievalQA` for consistent validation
    2.  Add metrics for sparse vector failure rate
    3.  Implement query routing based on question type
    4.  Add support for code change summarization before review
-   **Benefit:** Higher quality reviews with fewer false positives.

### 7. **Multi-Language Parser Support**

Extend language support beyond Go and TypeScript.

-   **TODO:**
    1.  Add Python parser plugin
    2.  Add Java parser plugin
    3.  Add Rust parser plugin
-   **Benefit:** Broader language support for diverse teams.

## Architecture Improvements (from Code Review)

### Resource Management

| Issue | Priority | Description |
|-------|----------|-------------|
| VectorStore.Close() | ✅ Done | Added Close() method to release gRPC connections |
| Context propagation | ✅ Done | Fixed context.Background() usage throughout RAG pipeline |
| Unbounded client map | Medium | Add cleanup for unused collection clients in vectorstore |

### Code Quality

| Issue | Priority | Description |
|-------|----------|-------------|
| Map growth in repomanager | Medium | Add cleanup for old mutexes in repoMux map |
| Job queue metrics | Medium | Add alerting when job queue is full |
| Pre-allocation | Low | Pre-allocate slices in hot paths |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.

## Changelog

### v0.3.0 (Feb 2025)
- GoFrame v0.23.2 compatibility
- Resource leak fixes (VectorStore.Close())
- Context propagation improvements
- Updated README with architecture documentation

### v0.2.0 (Jan 2025)
- Consensus review with multi-model synthesis
- Re-review command for validating previous suggestions
- HyDE context caching
- Dependency graph traversal for impact analysis

### v0.1.0 (Dec 2024)
- Initial release
- Structured line-specific reviews
- Intelligent RAG context caching
- Repository configuration support