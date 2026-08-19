# Architecture Overview

Code-Warden is a self-hosted GitHub App that reviews pull requests using an agent-based approach: parallel agent passes investigate the diff with grep + `read_file` against an isolated checkout. This document covers the component layout and how they connect.

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                           CODE-WARDEN (Application Layer)                       │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐ │
│  │   GitHub App   │  │  MCP Server    │  │  Job System    │  │  Repo Manager  │ │
│  │  (webhooks)    │  │  (tools)       │  │  (dispatcher)  │  │  (git ops)    │ │
│  └───────┬────────┘  └───────┬────────┘  └───────┬────────┘  └───────┬────────┘ │
│          │                   │                   │                   │          │
│          └───────────────────┴───────────────────┴───────────────────┘          │
│                                      │                                           │
│                          ┌───────────▼───────────┐                              │
│                          │  Agent Review Runner  │                              │
│                          │  (multi-angle passes) │                              │
│                          └───────────┬───────────┘                              │
│                                      │                                           │
│                          ┌───────────▼───────────┐                              │
│                          │    Agent Orchestrator │                              │
│                          │    (/implement cmd)   │                              │
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
| `agent/` | Agent loop, tool registry, governance, observer |
| `chains/` | Parallel map-reduce with quorum (used for multi-angle review) |
| `gitutil/` | Pure-Go shallow clone for isolated workspaces |

### Code-Warden (Application Layer)

| Component | Purpose | Location |
|-----------|---------|----------|
| **GitHub App** | Webhook handling, PR/issue processing | `internal/github/`, `internal/server/` |
| **Agent Review** | Multi-angle agent-based review runner | `internal/agent/review/` |
| **MCP Server** | Tools for AI agents | `internal/mcp/` |
| **Agent Orchestrator** | Session management, workspace isolation | `internal/agent/` |
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

The `/implement` flow is documented in [IMPLEMENT_ARCHITECTURE.md](./IMPLEMENT_ARCHITECTURE.md).

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

### MCP Tools

```go
type Tool interface {
    Name() string
    Description() string
    ParametersSchema() map[string]any
    Execute(ctx context.Context, args map[string]any) (any, error)
}
```

---

- [SETUP.md](./SETUP.md) — Deployment and first-run guide
- [IMPLEMENT_ARCHITECTURE.md](./IMPLEMENT_ARCHITECTURE.md) — `/implement` flow and agent design
- [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) — Common issues and fixes
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — How to contribute
