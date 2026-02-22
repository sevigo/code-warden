# RAG Architecture for Code Review

This document describes the Retrieval-Augmented Generation (RAG) pipeline used by Code-Warden for hallucination-resistant code reviews.

## Overview

Code-Warden uses a **multi-stage retrieval pipeline** to gather comprehensive context before generating code reviews. This architecture is specifically designed to minimize LLM hallucinations by grounding all findings in retrieved repository context.

## Pipeline Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        CODE-WARDEN RAG PIPELINE                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  [PR Diff] ──► [5-Stage Parallel Context Building] ──► [Context Assembly]   │
│                                                             │                │
│  Context Stages (parallel):                                ▼                │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────┐                 │
│  │ 1. Architectural│  │ 2. HyDE         │  │ 3. Impact   │                 │
│  │    Context      │  │    Context      │  │    Context  │                 │
│  │    (dir summaries)│  │    (query     │  │    (dependency │              │
│  │                 │  │     expansion) │  │     graph)  │                 │
│  └─────────────────┘  └─────────────────┘  └─────────────┘                 │
│  ┌─────────────────┐  ┌─────────────────┐                                  │
│  │ 4. Description  │  │ 5. Definitions  │                                  │
│  │    Context      │  │    Context      │                                  │
│  │    (MultiQuery) │  │    (symbol      │                                  │
│  │                 │  │     resolution) │                                  │
│  └─────────────────┘  └─────────────────┘                                  │
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

**Location:** `internal/rag/rag_hyde.go`

**Key Feature:** Cached by patch hash to avoid redundant LLM calls

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

**Mechanism:** MultiQuery retrieval - generates multiple query variations from PR description

**Location:** `internal/rag/rag_context.go:260-310`

### 5. Definitions Context
**Purpose:** Resolve type/function definitions for symbols in the diff

**Mechanism:**
1. Extract symbols from patch using regex patterns
2. Query `DefinitionRetriever` for each symbol
3. Include full definition text in context

**Location:** `internal/rag/rag_context.go:84-148`

## Hallucination Reduction Mechanisms

### Layer 1: Empty Context Detection
**File:** `internal/rag/rag_review.go`

When no context is retrieved, the system:
1. Logs a `HIGH HALLUCINATION RISK` warning
2. Injects explicit warnings into the prompt
3. Adds disclaimer to review summary

```go
if contextString == "" && definitionsContext == "" {
    r.logger.Warn("HIGH HALLUCINATION RISK: no context retrieved...")
    // Inject warnings into prompt
}
```

### Layer 2: Snippet Relevance Validation
**File:** `internal/rag/rag_context.go:312-327`

Uses a fast LLM to validate retrieved snippets:
```go
func validateSnippetRelevance(ctx, snippet, prContext) bool {
    // Returns false if snippet is irrelevant
    // Fails OPEN (includes snippet) if validator unavailable
}
```

### Layer 3: Document Deduplication
**File:** `internal/rag/rag_context.go:572-587`

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
**File:** Multiple locations

Combines dense embeddings with sparse (BM25-style) vectors:
```go
sparseVec, _ := sparse.GenerateSparseVector(ctx, query)
docs, _ := store.SimilaritySearch(ctx, query, 5,
    vectorstores.WithSparseQuery(sparseVec))
```

### Layer 5: Symbol Resolution
**File:** `internal/rag/rag_context.go:150-182`

Uses exact-match filters for definition lookup:
```go
docs, _ := defRetriever.GetDefinition(ctx, symbol)
// Filter: identifier=symbol AND is_definition=true
// Now includes sparse vector for better exact matching
```

## Key Files

| File | Purpose |
|------|---------|
| `internal/rag/rag.go` | Service interface and initialization |
| `internal/rag/rag_context.go` | Multi-stage context building |
| `internal/rag/rag_review.go` | Review generation with validation |
| `internal/rag/rag_hyde.go` | HyDE query expansion |
| `internal/rag/rag_impact.go` | Dependency graph traversal |
| `internal/rag/rag_rereview.go` | Feedback-driven re-review |
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

1. Integrate `chains.ValidatingRetrievalQA` for consistent validation
2. Add metrics for sparse vector failure rate
3. Increase HyDE cache hash from 8 to 16 bytes
