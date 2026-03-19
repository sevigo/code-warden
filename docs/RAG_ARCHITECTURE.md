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

**Mechanism:** MultiQuery retrieval - generates multiple query variations from PR description

**Location:** `internal/rag/contextpkg/builder.go:168-203`

### 5. Definitions Context
**Purpose:** Resolve type/function definitions for symbols in the diff

**Mechanism:**
1. Extract symbols from patch using regex patterns
2. Query `DefinitionRetriever` for each symbol
3. Include full definition text in context
4. Cap at ~15k characters to prevent token budget exhaustion
5. Prioritize interfaces/types over implementations

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
**File:** `internal/rag/contextpkg/symbols.go`

Uses exact-match filters for definition lookup:
```go
docs, _ := defRetriever.GetDefinition(ctx, symbol)
// Filter: identifier=symbol AND is_definition=true
// Now includes sparse vector for better exact matching
// Capped at ~15k characters to prevent token exhaustion
```

### Layer 6: Test Filtering
**File:** `internal/rag/contextpkg/format.go`

Test files are filtered from production code retrieval:
```go
func filterTestDocs(docs []schema.Document) []schema.Document {
    // Removes documents with is_test: true from impact/description context
    // Tests are only retrieved via test coverage stage
}
```

### Layer 7: Test Coverage Linkage
**File:** `internal/rag/index/test_linkage.go`

During indexing, test files are analyzed to extract tested symbols:
```go
// Test file metadata includes:
// - tested_symbols: ["UserService", "CreateUser"]
// - source_file: "internal/rag/service.go"
// These are used to find relevant tests during review
```

## Key Files

| File | Purpose |
|------|---------|
| `internal/rag/contextpkg/builder.go` | Multi-stage context building orchestration |
| `internal/rag/contextpkg/arch.go` | Architecture context from pre-computed summaries |
| `internal/rag/contextpkg/hyde.go` | HyDE query expansion with language-aware prompts |
| `internal/rag/contextpkg/symbols.go` | Definition resolution with prioritization |
| `internal/rag/contextpkg/test_coverage.go` | Test coverage retrieval for changed symbols |
| `internal/rag/contextpkg/format.go` | Context assembly and formatting |
| `internal/rag/index/indexer.go` | Document chunking and metadata extraction |
| `internal/rag/index/definitions.go` | Type/function definition extraction |
| `internal/rag/index/test_linkage.go` | Test-to-code symbol linkage extraction |
| `internal/rag/review/reviewer.go` | Review generation with validation |
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

1. Add metrics for sparse vector failure rate
2. Cache architecture and impact retrieval per diff hash
3. Implement pre-computed call graph edges for accurate impact analysis
