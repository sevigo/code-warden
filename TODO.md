# TODO

This document outlines the development roadmap for Code-Warden. It tracks pending features and future ideas.

## 🚀 Next Up: Immediate Priorities

### 1. Create a Simple Web UI for Status & Onboarding

Provide a user-friendly way to see what the app is doing and what repositories are managed.

- **TODO:**
    1.  Add frontend routes in `internal/server/router.go`
    2.  Build a status page listing all repositories with last indexed SHA
    3.  Show job history with status and PR links
- **Benefit:** Improves transparency and user experience.

### 2. Add Godoc Documentation

Improve package-level documentation for better discoverability.

- **TODO:**
    1.  Add godoc comments to `internal/storage/` interfaces
    2.  Document `internal/rag/` service methods
    3.  Document `internal/jobs/` dispatcher and worker
    4.  Add package-level documentation
- **Benefit:** Better developer experience and API discoverability.

### 3. Implement Resource Lifecycle Management

Ensure long-term stability with garbage collection.

- **TODO:**
    1.  Create a "Janitor" background service
    2.  TTL-based cleanup for old repositories (Qdrant collections, disk files, DB records)
    3.  Handle GitHub App uninstallation events
- **Benefit:** Prevents resource leaks and controls operational costs.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.
