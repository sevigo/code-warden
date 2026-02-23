# Code-Warden Code Review Findings

## Critical Issues (FIXED - GoFrame v0.23.2 Compatibility)

These issues were fixed in the `feat/goframe-api-compatibility` branch:

| File | Line | Issue | Status |
|------|------|-------|--------|
| `internal/wire/wire.go` | 150-152 | `NewDependencyRetriever` returns error - removed unused function | ✅ Fixed |
| `internal/rag/rag_impact.go` | 25 | `NewDependencyRetriever` returns error | ✅ Fixed |
| `internal/rag/rag_context.go` | 390 | `NewDefinitionRetriever` returns error | ✅ Fixed |
| `internal/rag/rag_review.go` | 85-88 | `chains.NewLLMChain` returns error | ✅ Fixed |
| `internal/rag/rag_question.go` | 36 | `chains.NewRetrievalQA` returns error | ✅ Fixed |
| `internal/rag/reuse_detector.go` | 299, 365 | `chains.NewLLMChain` returns error | ✅ Fixed |

## High Severity Issues

| File | Line | Issue | Fix |
|------|------|-------|-----|
| `internal/app/app.go` | 80-102 | `VectorStore.Close()` not called in `Stop()` - resource leak | Add VectorStore cleanup |
| `internal/storage/vectorstore.go` | 54-63 | `qdrantVectorStore` lacks `Close()` method to clean up cached clients | Add Close() method |
| `internal/rag/rag.go` | 108 | Using `context.Background()` in `getOrCreateLLM` instead of passed context | Use caller context |
| `internal/rag/rag_index.go` | 303 | `SplitDocuments(context.Background(), ...)` - should use request context | Pass context parameter |
| `internal/rag/rag_index.go` | 311 | `GenerateSparseVector(context.Background(), ...)` - should use request context | Pass context parameter |
| `internal/wire/wire.go` | 264-272 | Error from `promptMgr.Render()` is silently ignored with just debug log | Handle or propagate error |
| `internal/rag/rag_review.go` | 85-89 | `prompts.NewPromptTemplate()` may fail if template is invalid - no error handling | Add validation |

## Medium Severity Issues

| File | Issue |
|------|-------|
| `internal/jobs/dispatcher.go` | Job queue is bounded at 100 but no metrics/alerting when jobs are rejected |
| `internal/jobs/review.go` | Potential race between `GetLatestReviewForPR` check and subsequent operations |
| `internal/rag/rag_context.go` | Multiple pre-compiled regex patterns already good - no changes needed |
| `internal/storage/vectorstore.go` | Client map grows unbounded - no cleanup of unused collection clients |
| `internal/repomanager/manager.go` | `repoMux` map grows unbounded - no cleanup of old mutexes |

## Performance Issues

| File | Line | Issue |
|------|------|-------|
| `internal/rag/rag_index.go` | 263-266 | `allDocs` slice not pre-allocated before appending |
| `internal/rag/rag_context.go` | 517-518 | `seen` map in `mergeAndDedup` could use `len(docs)` capacity |
| `internal/rag/rag_context.go` | 941-950 | `scored` slice pre-allocated correctly - good |

## Missing Godoc Comments (Key Packages)

### internal/storage/
- `VectorStore` interface methods lack documentation
- `ScopedVectorStore` interface - no docs
- `qdrantVectorStore` struct - no docs

### internal/rag/
- `Service` interface - has brief doc but methods lack docs
- `ragService` struct - no docs
- Many helper functions lack documentation

### internal/jobs/
- `dispatcher` struct - no docs
- `ReviewJob` struct - no docs

### internal/repomanager/
- `manager` struct - no docs
- `RepoManager` interface methods lack docs

### internal/core/
- Most types have good documentation already

## GoFrame Usage Patterns (Correct)

The codebase correctly uses:
- `vectorstores.WithCollectionName()` for collection scoping
- `storage.ForRepo()` for scoped vector store access
- `documentloaders.NewGit()` with proper options
- `textsplitter.NewCodeAware()` with tokenizer adapter
- Proper error handling in most places

## Completed Fixes (Phase 1)

1. **Updated `wire.go`** - Removed unused `provideDependencyRetriever` function
2. **Updated `rag_impact.go`** - Handle `NewDependencyRetriever` error
3. **Updated `rag_context.go`** - Handle `NewDefinitionRetriever` error
4. **Updated `rag_review.go`** - Handle `NewLLMChain` error
5. **Updated `rag_question.go`** - Handle `NewRetrievalQA` error
6. **Updated `reuse_detector.go`** - Handle `NewLLMChain` errors (2 places)
7. **Regenerated `wire_gen.go`** - Wire code regenerated

## Remaining Work

### Phase 2: Resource Management

1. Add `Close()` method to `qdrantVectorStore`
2. Update `App.Stop()` to close VectorStore
3. Add cleanup for cached clients in vectorstore

### Phase 3: Context Propagation

1. Pass context through `ProcessFile` method
2. Use caller context in `getOrCreateLLM`
3. Update sparse vector generation to use request context

### Phase 4: Documentation

1. Add godoc comments to all public interfaces
2. Document thread-safety guarantees
3. Document lifecycle management (Close methods)