# TODO

This document outlines the development roadmap for Code-Warden. It tracks completed features, immediate priorities, and future ideas.

## ✅ Recently Completed

-   **Performance & Stability Optimizations:**
    -   **Resolution:** Pre-allocated `allDocs` slice in `rag_index.go`, implemented `ClearLocks` in `repomanager`, and added queue saturation alerting.
    -   **Benefit:** Reduced GC pressure and prevented unbounded memory growth for long-running sessions.

-   **Resource Leak Fixes:**
    -   **Resolution:** Added `Close()` method to `VectorStore` to properly release gRPC connections.
    -   **Benefit:** Clean application shutdown.

-   **Context Propagation Improvements:**
    -   **Resolution:** Fixed `context.Background()` usage throughout the RAG pipeline.
    -   **Benefit:** Proper cancellation support during heavy indexing.

-   **GitHub Suggested Changes:**
    -   **Resolution:** Added `code_suggestion` field to `core.Suggestion`, implemented GitHub Review API suggestion format (````suggestion` code fence), and updated prompts.
    -   **Benefit:** Developers can accept AI feedback with a single click.

-   **Advanced RAG Enhancements:**
    -   **Resolution:** Integrated `chains.ValidatingRetrievalQA` for consistent validation.
    -   **Benefit:** Higher quality reviews with fewer false positives.

-   **Unbounded Client Map Cleanup:**
    -   **Resolution:** Added cleanup for unused collection clients in vectorstore.
    -   **Benefit:** Prevents memory leaks from unbounded client map.


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

-   **Benefit:** More stable long-running operations. (✅ Done)

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
| Unbounded client map | ✅ Done | Add cleanup for unused collection clients in vectorstore |
| gitutil context | ✅ Done | Added context propagation to GetRemoteHeadSHA for proper cancellation |

### Code Quality

| Issue | Priority | Description |
|-------|----------|-------------|
| Map growth in repomanager | ✅ Done | Add cleanup for old mutexes in repoMux map |
| Job queue metrics | ✅ Done | Add alerting when job queue is full |
| Pre-allocation | ✅ Done | Pre-allocate slices in hot paths |
| Comment deletion handling | ✅ Done | Ignore comment deletion events in webhook handler |

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