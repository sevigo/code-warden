# RAG Architecture for Code Review

This document describes the Retrieval-Augmented Generation (RAG) pipeline used by Code-Warden for hallucination-resistant code reviews.

## Overview

Code-Warden uses a **6-stage retrieval pipeline** to gather comprehensive context before generating code reviews. This architecture is specifically designed to minimize LLM hallucinations by grounding all findings in retrieved repository context.

## Pipeline Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        CODE-WARDEN RAG PIPELINE                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  [PR Diff] ──► [6-Stage Parallel Context Building] ──► [Context Assembly]   │
│                                                             │                │
│  Context Stages (parallel):                                ▼                │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────┐                 │
│  │ 1. Architectural│  │ 2. HyDE         │  │ 3. Impact   │                 │
│  │    Context      │  │    Context      │  │    Context  │                 │
│  │  (dir summaries)│  │    (query       │  │ (dependency │                 │
│  │                 │  │     expansion)  │  │    graph)   │                 │
│  └─────────────────┘  └─────────────────┘  └─────────────┘                 │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────┐                 │
│  │ 4. Description  │  │ 5. Definitions  │  │ 6. Test     │                 │
│  │    Context      │  │    Context      │  │    Coverage │                 │
│  │  (MultiQuery +  │  │    (symbol      │  │  (relevant  │                 │
│  │ commit messages)│  │     resolution) │  │    tests)   │                 │
│  └─────────────────┘  └─────────────────┘  └─────────────┘                 │
│                                                             │                │
│                                                             ▼                │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │              Context Validation & Deduplication                      │   │
│  │  - Parent-aware document keys (prevents chunk duplicates)           │   │
│  │  - Reranking with BM25 pre-filter                                   │   │
│  │  - LLM-based snippet relevance validation                           │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                             │                │
│                                                             ▼                │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    Prompt Assembly + LLM Call                        │   │
│  │  - Warning injected if context is empty                             │   │
│  │  - Structured XML output with strict parsing                        │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Context Stages Detail

### 1. Architectural Context
**Purpose:** Provide high-level understanding of affected modules

**Source:** Pre-computed directory summaries stored in Qdrant with `chunk_type: "arch"`

**Retrieval:**
```go
GetArchContextForPaths(ctx, scopedStore, filePaths)
```

**When Empty:** Review proceeds but LLM is warned about missing architectural context

### 2. HyDE Context (Hypothetical Document Embeddings)
**Purpose:** Expand queries to find semantically related code

**Process:**
1. Generate hypothetical code snippet from patch using LLM
2. Search using generated snippet as query
3. Apply reranking with BM25 pre-filtering
4. Filter out test documents (is_test: true)

**Location:** `internal/rag/contextpkg/hyde.go`

**Key Features:**
- Cached by patch hash to avoid redundant LLM calls
- Language-aware prompt generation (Go, TypeScript, Python, etc.)
- Per-file cache keys prevent cross-file collisions

### 3. Impact Context
**Purpose:** Identify downstream code that may be affected by changes

**Mechanism:** Uses `DependencyRetriever` to find dependents (code that imports changed packages)

**Retrieval:**
```go
retriever := vectorstores.NewDependencyRetriever(store)
network, _ := retriever.GetContextNetwork(ctx, packageName, imports)
// network.Dependents contains affected code
```

### 4. Description Context
**Purpose:** Find code related to the PR's stated intent

**Mechanism:** MultiQuery retrieval — generates multiple query variations from a combined description that includes the PR title, body, and the first line of each commit message. Commit messages are fetched from the GitHub API before the review starts and added to the event context.

**Location:** `internal/rag/contextpkg/builder.go`, `internal/rag/review/review.go:buildPRDescription`

### 5. Definitions Context
**Purpose:** Resolve type/function definitions for symbols in the diff

**Mechanism:**
1. Extract symbols from patch using regex patterns
2. **Fast path:** exact Qdrant payload filter (`chunk_type=definition`, `identifier=symbol`) — precise, no false positives
3. **Fallback:** semantic search via `DefinitionRetriever` if exact match returns nothing
4. Include full definition text in context
5. Cap at ~15k characters to prevent token budget exhaustion
6. Prioritize interfaces/types over implementations

**Location:** `internal/rag/contextpkg/symbols.go`

### 6. Test Coverage Context
**Purpose:** Show relevant tests for changed code to help identify edge cases

**Mechanism:**
1. Extract symbols from definitions context
2. Search for test chunks with `tested_symbols` metadata matching
3. Include test chunks that reference changed symbols

**Metadata on test chunks:**
```json
{
  "is_test": true,
  "tested_symbols": ["UserService", "CreateUser"],
  "source_file": "internal/rag/service.go"
}
```

**Location:** `internal/rag/contextpkg/test_coverage.go`

## Hallucination Reduction Mechanisms

### Layer 1: Empty Context Detection
**File:** `internal/rag/review/reviewer.go`

When no context is retrieved, the system:
1. Logs a `HIGH HALLUCINATION RISK` warning
2. Injects explicit warnings into the prompt
3. Adds disclaimer to review summary

### Layer 2: Snippet Relevance Validation
**File:** `internal/rag/contextpkg/format.go`

Uses a fast LLM to validate retrieved snippets before including them in context. Fails open — if the validator is unavailable, the snippet is included rather than dropped.

### Layer 3: Document Deduplication
**File:** `internal/rag/contextpkg/format.go`

Parent-aware document keys prevent redundant context:
```go
func getDocKey(doc) string {
    if parentID, ok := doc.Metadata["parent_id"]; ok {
        return parentID  // Use parent key for all child chunks
    }
    // Fallback to source+identifier or content hash
}
```

### Layer 4: Hybrid Search
**Files:** `internal/rag/contextpkg/`, `internal/rag/question/`

Combines dense embeddings with code-aware sparse vectors (camelCase/snake_case tokenization via FNV hashing). Applied at every retrieval site — HyDE, description, test coverage, symbol lookup:
```go
sparseVec, _ := sparse.GenerateSparseVector(ctx, query)
docs, _ := store.SimilaritySearch(ctx, query, 5,
    vectorstores.WithSparseQuery(sparseVec))
```

### Layer 5: Exact Symbol Lookup
**File:** `internal/rag/contextpkg/symbols.go`

Definition lookup uses an exact Qdrant payload filter as the first pass before falling back to semantic search:
```go
// Fast path: exact filter
exactDocs, _ := store.SimilaritySearch(ctx, symbol, 1,
    vectorstores.WithFilters(map[string]any{
        "chunk_type": "definition",
        "identifier": symbol,
    }))
// Fallback: semantic search via DefinitionRetriever
```

### Layer 6: Test File Filtering
**File:** `internal/rag/contextpkg/format.go`

Test files (`is_test: true`) are filtered out of impact and description context. They are only surfaced through the dedicated test coverage stage, which retrieves them by matched symbol rather than semantic similarity.

### Layer 7: Test Coverage Linkage
**File:** `internal/rag/index/test_linkage.go`

During indexing, test files are analyzed to extract which symbols they exercise. This metadata is stored alongside the chunk:
```json
{
  "is_test": true,
  "tested_symbols": ["UserService", "CreateUser"],
  "source_file": "internal/rag/service.go"
}
```
During review, stage 6 uses these fields to pull in tests that directly exercise changed symbols.

## Key Files

| File | Purpose |
|------|---------|
| `internal/rag/contextpkg/builder.go` | 6-stage context building orchestration |
| `internal/rag/contextpkg/arch.go` | Stage 1: architecture context from pre-computed summaries |
| `internal/rag/contextpkg/hyde.go` | Stage 2: HyDE query expansion with language-aware prompts |
| `internal/rag/contextpkg/symbols.go` | Stage 5: definition resolution (exact filter + semantic fallback) |
| `internal/rag/contextpkg/test_coverage.go` | Stage 6: test coverage retrieval for changed symbols |
| `internal/rag/contextpkg/format.go` | Context assembly, deduplication, validation, test filtering |
| `internal/rag/review/review.go` | `buildPRDescription` — merges PR title/body/commit messages for stage 4 |
| `internal/rag/index/indexer.go` | Document chunking, sparse vector generation, metadata extraction |
| `internal/rag/index/definitions.go` | Type/function definition extraction per language |
| `internal/rag/index/test_linkage.go` | Test-to-code symbol linkage extraction during indexing |
| `internal/rag/review/reviewer.go` | Review generation, empty context detection, LLM call |
| `internal/llm/prompts/*.prompt` | Prompt templates |

## GoFrame Patterns Used

Code-Warden extensively uses goframe patterns:

| Pattern | Usage |
|---------|-------|
| `schema.Retriever` | All document retrieval |
| `schema.Reranker` | Result reranking |
| `vectorstores.VectorStore` | Qdrant operations |
| `chains.LLMChain[T]` | Typed LLM calls with output parsing |
| `chains.RetrievalQA` | Question answering |
| `chains.MapReduceChain` | Consensus review generation |
| `vectorstores.Option` | Functional options for searches |
| `textsplitter.TextSplitter` | Code-aware chunking |

## Configuration

Control RAG behavior via config:

```yaml
ai:
  enable_hyde: true      # Enable HyDE query expansion
  generator_model: "gemma3:latest"
  fast_model: "gemma3:latest"  # For validation
  embedder_model: "nomic-embed-text"
```

## Monitoring

Key log messages to monitor:

- `HIGH HALLUCINATION RISK` - Empty context detected
- `sparse vector generation failed` - Sparse search degraded
- `no documents found for query` - Retrieval gap
- `context validation failed` - Validator LLM error

## Performance Optimization

1. **Parallel Context Building:** All 5 stages run concurrently
2. **HyDE Caching:** Patch hash → snippet cache
3. **BM25 Pre-filtering:** Reduces reranker load
4. **Semaphore Limits:** Prevents resource exhaustion

## Future Improvements

See [TODO.md](../TODO.md) for the full roadmap. RAG-specific items:

- Add metrics for sparse vector failure rate and retrieval hit rates per stage
- Cache architecture and impact retrieval per diff hash
- Implement pre-computed call graph edges for more accurate impact analysis
- Query decomposition / multi-query retrieval for complex PR descriptions
- Contextual chunk compression — strip boilerplate from retrieved chunks before inserting into prompt
- Retrieval eval set to measure recall@k and tune retrieval parameters offline
