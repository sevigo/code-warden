# Architecture Overview

Code-Warden is a self-hosted GitHub App that reviews pull requests using an agent-based approach: parallel agent passes investigate the diff with grep + `read_file` against an isolated checkout. This document covers the component layout and how they connect.

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                           CODE-WARDEN (Application Layer)                       │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐ │
│  │   GitHub App   │  │  Job System    │  │  Repo Manager  │  │ Review Tools   │ │
│  │  (webhooks)    │  │  (dispatcher)  │  │  (git ops)     │  │ (read-only)    │ │
│  └───────┬────────┘  └───────┬────────┘  └───────┬────────┘  └───────┬────────┘ │
│          │                   │                   │                   │          │
│          └───────────────────┴───────────────────┴───────────────────┘          │
│                                      │                                           │
│                          ┌───────────▼───────────┐                              │
│                          │  Agent Review Runner  │                              │
│                          │  (multi-angle passes) │                              │
│                          └───────────┬───────────┘                              │
│                                      │                                           │
└──────────────────────────────────────┼───────────────────────────────────────────┘
                                        │ uses
                     ┌──────────────────▼──────────────────┐
                     │            GoFrame                  │
                     │         (Library Layer)             │
                     │  ┌─────────────────────────────┐   │
                     │  │          llms               │   │
                     │  │  (Ollama/Gemini/OpenAI)     │   │
                     │  └─────────────────────────────┘   │
                     │  ┌─────────────────────────────┐   │
                     │  │          agent              │   │
                     │  │  (loop, registry, chains)   │   │
                     │  └─────────────────────────────┘   │
                     │  ┌─────────────────────────────┐   │
                     │  │        gitutil              │   │
                     │  │  (clone into workspace)     │   │
                     │  └─────────────────────────────┘   │
                     └─────────────────────────────────────┘
```

## Separation of Concerns

### GoFrame (Library Layer)

GoFrame provides the agent runtime and LLM abstraction:

| Package | Purpose |
|---------|---------|
| `llms/` | LLM abstraction — Model interface, Ollama/Gemini/OpenAI implementations |
| `agent/` | Agent loop and tool registry |
| `chains/` | Parallel map-reduce with quorum (used for multi-angle review) |
| `gitutil/` | Pure-Go shallow clone for isolated workspaces |

### Code-Warden (Application Layer)

| Component | Purpose | Location |
|-----------|---------|----------|
| **GitHub App** | Webhook handling and PR processing | `internal/github/`, `internal/server/` |
| **Agent Review** | Multi-angle agent-based review runner | `internal/agent/review/` |
| **Review Tools** | Workspace-bound read-only tools | `internal/agent/reviewtools/` |
| **Job System** | Background job dispatch and execution | `internal/jobs/` |
| **Storage** | PostgreSQL persistence | `internal/storage/` |
| **Repo Manager** | Git clone, sync, diff calculation | `internal/repomanager/` |

## Data Flow

### `/review` Command Flow

```
┌────────────┐    ┌────────────┐    ┌────────────┐    ┌────────────┐
│ PR Comment │───►│ Webhook    │───►│ Job Queue  │───►│ ReviewJob  │
│ "/review"  │    │ Handler    │    │ Dispatcher │    │ (worker)   │
└────────────┘    └────────────┘    └────────────┘    └─────┬──────┘
                                                              │
                       ┌───────────────────────────────────────┤
                       │                                       │
                       ▼                                       ▼
              ┌────────────────┐                    ┌────────────────┐
              │  RepoManager   │                    │ Agent Review   │
              │  (git clone)   │                    │ Runner         │
              └────────────────┘                    └───────┬────────┘
                                                            │
                                     ┌──────────────────────┼──────────────────┐
                                     ▼                      ▼                  ▼
                              ┌──────────────┐      ┌──────────────┐   ┌──────────────┐
                              │ Bug pass     │      │ Security     │   │ Perf/Conv    │
                              │ (grep+read)  │      │ (grep+read)  │   │ (grep+read)  │
                              └──────┬───────┘      └──────┬───────┘   └──────┬───────┘
                                     └─────────┬────────────┴───────────┬──────┘
                                               ▼                        ▼
                                       ┌────────────────┐      ┌────────────────┐
                                       │ merge + dedup  │      │ GitHub Client  │
                                       │ (file:line)    │      │ (post comment) │
                                       └────────────────┘      └────────────────┘
```

## Key Interfaces

### Storage Layer

```go
type Store interface {
    CreateRepository(ctx, repo) error
    GetRepository(ctx, id) (*Repository, error)
    SaveReview(ctx, review) error
    // ...
}
```

### Agent Review Runner

```go
type Runner struct { /* llm, promptMgr, tools, angles, logger */ }
func (r *Runner) Run(ctx, params) (*Result, error)
```

- [SETUP.md](./SETUP.md) — Deployment and first-run guide
- [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) — Common issues and fixes
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — How to contribute
