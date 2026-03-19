# Code-Warden Architecture Overview

This document provides a high-level overview of Code-Warden's architecture, component relationships, and the separation between application and library layers.

## Project Overview

Code-Warden is a self-hosted GitHub App that performs contextual code reviews using Large Language Models (LLMs). It uses a RAG (Retrieval-Augmented Generation) pipeline with Qdrant vector store to provide repository-aware feedback when triggered by `/review`, `/rereview`, or `/implement` commands on pull requests and issues.

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                           CODE-WARDEN (Application Layer)                       │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐ │
│  │   GitHub App   │  │  MCP Server    │  │  Job System    │  │  Repo Manager  │ │
│  │  (webhooks)    │  │  (JSON-RPC)    │  │  (dispatcher)  │  │  (git ops)    │ │
│  └───────┬────────┘  └───────┬────────┘  └───────┬────────┘  └───────┬────────┘ │
│          │                   │                   │                   │          │
│          └───────────────────┴───────────────────┴───────────────────┘          │
│                                      │                                           │
│                          ┌───────────▼───────────┐                              │
│                          │     RAG Service       │                              │
│                          │  (6-stage pipeline)   │                              │
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
                    │  │      VectorStore            │   │
                    │  │      (Qdrant impl)          │   │
                    │  └─────────────────────────────┘   │
                    │  ┌─────────────────────────────┐   │
                    │  │       Embedder              │   │
                    │  │    (Ollama/Gemini)          │   │
                    │  └─────────────────────────────┘   │
                    │  ┌─────────────────────────────┐   │
                    │  │    DocumentLoader           │   │
                    │  │    (Git, Parsers)           │   │
                    │  └─────────────────────────────┘   │
                    │  ┌─────────────────────────────┐   │
                    │  │        Chains               │   │
                    │  │  (RetrievalQA, etc)         │   │
                    │  └─────────────────────────────┘   │
                    └─────────────────────────────────────┘
```

## Separation of Concerns

### GoFrame (Library Layer)

GoFrame is a **RAG framework for code understanding** that provides:

| Package | Purpose | Scope |
|---------|---------|-------|
| `schema/` | Core data structures | Document, SparseVector, Retriever, Reranker interfaces |
| `llms/` | LLM abstraction | Model interface, Ollama/Gemini implementations |
| `embeddings/` | Vector embeddings | Embedder interface, batch processing, sparse vectors |
| `vectorstores/` | Vector database | VectorStore interface, Qdrant implementation, retrievers |
| `parsers/` | Language parsing | Parser plugins for Go, TypeScript, Markdown, etc. |
| `textsplitter/` | Code chunking | Code-aware text splitting with metadata propagation |
| `documentloaders/` | Document loading | Git repository loading with streaming |
| `chains/` | LLM chains | LLMChain, RetrievalQA, MapReduceChain |

**GoFrame does NOT include:**
- Application logic (GitHub webhooks, job queues)
- Agent orchestration (MCP server, OpenCode client)
- Business-specific tools (PR creation, issue management)
- Database persistence (PostgreSQL models)

### Code-Warden (Application Layer)

Code-Warden builds on GoFrame to provide:

| Component | Purpose | Location |
|-----------|---------|----------|
| **GitHub App** | Webhook handling, PR/issue processing | `internal/github/`, `internal/server/` |
| **RAG Service** | 6-stage context building for reviews | `internal/rag/` |
| **MCP Server** | JSON-RPC tools for AI agents | `internal/mcp/` |
| **Agent Orchestrator** | Session management, workspace isolation | `internal/agent/` |
| **Job System** | Background job dispatch and execution | `internal/jobs/` |
| **Storage** | PostgreSQL + Qdrant data access | `internal/storage/` |
| **Repo Manager** | Git clone, sync, diff calculation | `internal/repomanager/` |

## Component Relationships

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                         COMPONENT DEPENDENCY GRAPH                               │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│   GitHub Webhook                                                                 │
│        │                                                                         │
│        ▼                                                                         │
│   ┌─────────────┐     ┌─────────────┐     ┌─────────────┐                      │
│   │   Server    │────►│ Webhook     │────►│ Job         │                      │
│   │   (HTTP)    │     │ Handler     │     │ Dispatcher  │                      │
│   └─────────────┘     └─────────────┘     └──────┬──────┘                      │
│                                                  │                               │
│                                                  ▼                               │
│                                          ┌─────────────┐                        │
│                                          │ ReviewJob   │                        │
│                                          │ (worker)    │                        │
│                                          └──────┬──────┘                        │
│                                                 │                                │
│         ┌───────────────────────────────────────┼───────────────────────────┐  │
│         │                                       │                           │  │
│         ▼                                       ▼                           ▼  │
│   ┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌─────────────┐│
│   │ RepoManager │────►│ RAG Service │────►│ VectorStore │     │  GitHub     ││
│   │ (git ops)   │     │ (context)   │     │ (Qdrant)    │     │  Client     ││
│   └─────────────┘     └──────┬──────┘     └─────────────┘     └──────┬──────┘│
│                              │                                        │        │
│                              ▼                                        │        │
│                       ┌─────────────┐                                │        │
│                       │ LLM Client  │◄───────────────────────────────┘        │
│                       │ (Ollama/    │                                         │
│                       │  Gemini)     │                                         │
│                       └─────────────┘                                         │
│                                                                                  │
│   For /implement command:                                                       │
│                                                                                  │
│   ┌─────────────┐     ┌─────────────┐     ┌─────────────┐                      │
│   │ Agent       │────►│ MCP Server  │────►│ OpenCode    │                      │
│   │ Orchestrator│     │ (tools)     │     │ Client      │                      │
│   └─────────────┘     └──────┬──────┘     └─────────────┘                      │
│                              │                                                  │
│                              ▼                                                  │
│                       ┌─────────────┐                                          │
│                       │ RAG Service  │ (via review_code tool)                   │
│                       │ VectorStore  │                                          │
│                       └─────────────┘                                          │
│                                                                                  │
└─────────────────────────────────────────────────────────────────────────────────┘
```

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
            │  RepoManager   │                    │  RAG Service   │
            │  (git clone,   │                    │  (6-stage)     │
            │   diff calc)   │                    └───────┬────────┘
            └────────────────┘                            │
                                                          ▼
                                                 ┌────────────────┐
                                                 │  LLM Client    │
                                                 │  (review gen)  │
                                                 └───────┬────────┘
                                                         │
                                                         ▼
                                                 ┌────────────────┐
                                                 │ GitHub Client  │
                                                 │ (post comment) │
                                                 └────────────────┘
```

### `/implement` Command Flow

See [IMPLEMENT_ARCHITECTURE.md](./IMPLEMENT_ARCHITECTURE.md) for detailed flow.

## Key Interfaces

### Storage Layer

```go
// internal/storage/store.go
type Store interface {
    // PostgreSQL operations
    CreateRepository(ctx, repo) error
    GetRepository(ctx, id) (*Repository, error)
    SaveReview(ctx, review) error
    // ...
}

type ScopedVectorStore interface {
    // GoFrame VectorStore wrapper with repository scoping
    SimilaritySearch(ctx, query, k, opts...) ([]Document, error)
    SimilaritySearchWithScores(ctx, query, k, opts...) ([]ScoredDocument, error)
    AddDocuments(ctx, docs, opts...) ([]string, error)
    // ...
}
```

### RAG Service

```go
// internal/rag/rag.go
type Service interface {
    UpdateRepoContext(ctx, repo, repoPath) error
    GenerateReview(ctx, config, repo, event, diff, feedback) (*StructuredReview, error)
}
```

### MCP Tools

```go
// internal/mcp/server.go
type Tool interface {
    Name() string
    Description() string
    InputSchema() map[string]any
    Execute(ctx context.Context, args map[string]any) (any, error)
}
```

## Configuration

See [config.yaml.example](../config.yaml.example) for the full annotated configuration reference.

## Why Not Move Agentic Code to GoFrame?

A common question is whether the MCP server or agent orchestration should be moved to GoFrame. The answer is **no**, and here's why:

### Architectural Reasons

| Concern | Code-Warden | GoFrame |
|---------|-------------|---------|
| **Purpose** | Application (GitHub App) | Library (RAG framework) |
| **Scope** | Business logic, workflows | Reusable RAG primitives |
| **MCP Server** | Application-specific tools | ❌ Not RAG-related |
| **Agent Orchestrator** | Workflow management | ❌ Business logic |
| **GitHub Tools** | PR/issue operations | ❌ Application-specific |

### Key Principle: Separation of Concerns

1. **GoFrame** = RAG Library
   - Document loading, parsing, chunking
   - Embedding generation
   - Vector storage and retrieval
   - LLM chains for Q&A

2. **Code-Warden** = Application
   - GitHub webhook handling
   - Job queuing and dispatch
   - RAG service (uses GoFrame)
   - MCP server for agent tools
   - Agent orchestration

### What Could Be Extracted (Future Consideration)

If agentic patterns become more common across projects, consider creating a **separate shared package**:

```
mcp-go/                    # Potential separate package
├── server/               # JSON-RPC server
├── sse/                  # SSE transport
├── tool/                 # Tool interface
└── tools/                # Generic tool implementations
```

This would NOT belong in GoFrame because:
- MCP is an **agent protocol**, not a RAG concern
- Tools are application-specific (they depend on business logic)
- GoFrame's TODO.md confirms this focus (no MCP/agent items)

## Related Documentation

- [RAG_ARCHITECTURE.md](./RAG_ARCHITECTURE.md) - Detailed RAG pipeline documentation
- [IMPLEMENT_ARCHITECTURE.md](./IMPLEMENT_ARCHITECTURE.md) - `/implement` command flow
- [opencode-config.md](./opencode-config.md) - OpenCode agent configuration
- [../CLAUDE.md](../CLAUDE.md) - Development guidelines