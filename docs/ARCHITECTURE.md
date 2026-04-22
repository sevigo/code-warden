# Architecture Overview

Code-Warden is a self-hosted GitHub App that reviews pull requests using LLMs with full codebase context via a RAG pipeline. This document covers the component layout and how they connect.

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

GoFrame is a RAG framework for code understanding. It provides:

| Package | Purpose |
|---------|---------|
| `schema/` | Core data structures — Document, SparseVector, Retriever, Reranker interfaces |
| `llms/` | LLM abstraction — Model interface, Ollama/Gemini implementations |
| `embeddings/` | Vector embeddings — Embedder interface, batch processing, sparse vectors |
| `vectorstores/` | Vector database — VectorStore interface, Qdrant implementation, retrievers |
| `parsers/` | Language parsing — Parser plugins for Go, TypeScript, Markdown, etc. |
| `textsplitter/` | Code chunking — Code-aware text splitting with metadata propagation |
| `documentloaders/` | Document loading — Git repository loading with streaming |
| `chains/` | LLM chains — LLMChain, RetrievalQA, MapReduceChain |

GoFrame does **not** include: application logic (GitHub webhooks, job queues), agent orchestration, business-specific tools, or database persistence.

### Code-Warden (Application Layer)

| Component | Purpose | Location |
|-----------|---------|----------|
| **GitHub App** | Webhook handling, PR/issue processing | `internal/github/`, `internal/server/` |
| **RAG Service** | 6-stage context building for reviews | `internal/rag/` |
| **MCP Server** | JSON-RPC tools for AI agents | `internal/mcp/` |
| **Agent Orchestrator** | Session management, workspace isolation | `internal/agent/` |
| **Job System** | Background job dispatch and execution | `internal/jobs/` |
| **Storage** | PostgreSQL + Qdrant data access | `internal/storage/` |
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

type ScopedVectorStore interface {
    SimilaritySearch(ctx, query, k, opts...) ([]Document, error)
    SimilaritySearchWithScores(ctx, query, k, opts...) ([]ScoredDocument, error)
    AddDocuments(ctx, docs, opts...) ([]string, error)
    // ...
}
```

### RAG Service

```go
type Service interface {
    UpdateRepoContext(ctx, repo, repoPath) error
    GenerateReview(ctx, config, repo, event, diff, feedback) (*StructuredReview, error)
}
```

### MCP Tools

```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() map[string]any
    Execute(ctx context.Context, args map[string]any) (any, error)
}
```

---

- [SETUP.md](./SETUP.md) — Deployment and first-run guide
- [INDEXING.md](./INDEXING.md) — Chunk types, metadata schema, debugging retrieval
- [IMPLEMENT_ARCHITECTURE.md](./IMPLEMENT_ARCHITECTURE.md) — `/implement` flow and agent design
- [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) — Common issues and fixes
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — How to contribute