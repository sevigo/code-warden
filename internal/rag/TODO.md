# RAG Package TODO

## High Priority

### ~~Fix `mergeChunksForFile` O(n²) String Copying~~ ✅
- [x] Track only the last `maxOverlap` bytes instead of calling `merged.String()` per iteration
- [x] Benchmark before/after with large chunk counts

### ~~Fix `buildContextForPrompt` Input Slice Mutation~~ ✅
- [x] Replace `unique := docs[:0]` with `unique := make([]schema.Document, 0, len(docs))`
- [x] Audit other functions for similar in-place slice mutations

### ~~Fix `ensureReviewsDir` Relative Path from CWD~~ ✅
- [x] Derive reviews directory from repo path or a configured data directory
- [x] `reviewsDir := filepath.Join(filepath.Dir(repo.ClonePath), "reviews")`

### ~~Fix `UpdateRepoContext` Missing File Hash Updates~~ ✅
- [x] Call `r.store.UpsertFiles` after processing changed files (mirrors `SetupRepoContext`)
- [ ] Add integration test to verify smart-scan skips re-indexed files

### ~~Fix `mapKeysToSlice` Non-Deterministic Truncation~~ ✅
- [x] Sort map keys before truncating to `maxLen`
- [x] Ensures consistent symbol resolution across runs

### ~~Fix `fallbackConcat` Token Estimation~~ ✅
- [x] Change `tokensPerChar = 3` to `charsPerToken = 4` (standard ratio is ~4 chars/token)
- [x] Current code allows ~3× more content than the budget intends

## Medium Priority

### ~~Deduplicate LLM Instance Creation (`getOrCreateLLM`)~~ ✅
- [x] Use `singleflight.Group` to prevent concurrent creation of same model

### ~~Make `saveConsensusArtifact` Synchronous~~ ✅
- [x] Remove `go` prefix — it's just a file write, negligible cost
- [x] Prevents goroutine leaks or incomplete writes if process exits

### ~~Fix `fetchImpactResults` Map Race Condition~~ ✅
- [x] `depResults` is read concurrently with potential ongoing writes if `wg.Wait()` has a race
- [x] Alternatively, the returned map is exposed to data races by callers. Move `wg.Wait()` and safe handoff

### Add Test Coverage for `SetupRepoContext`
- [ ] Test worker pool shutdown on context cancellation
- [ ] Test batch flushing at boundary conditions (exactly `batchSize` docs)
- [ ] Test deletion pruning for removed files

### Parse `getConsensusTimeout` Once at Construction
- [ ] Move `time.ParseDuration` to `NewService` or first call
- [ ] Store parsed duration in `ragService` struct

## Low Priority

### Replace `containsString` with Map Lookup
- [ ] Use `map[string]struct{}` in `extractDocMetadata` for O(1) lookups
- [ ] Convert `info.Files` and `info.Symbols` to sets where possible

### Extract Shared SHA256 Hashing Helper
- [ ] Deduplicate `sha256.Sum256 → hex.EncodeToString` pattern across 4+ locations
- [ ] Parameterize truncation length (8 bytes vs 16 bytes vs full)

### ~~Remove `enrichAnswerWithContext` No-Op~~ ✅
- [x] Either implement conversation history support or remove the wrapper
- [x] Currently returns input unchanged, adding dead code

### ~~Remove `defer close(sem)` in `GenerateComparisonSummaries`~~ ✅
- [x] Channel GC handles cleanup; closing a shared channel can panic if goroutines are still writing
- [x] Safe today (after `g.Wait()`) but unnecessarily fragile

## Architecture: Split `ragService` God Object

The current `ragService` has 13+ methods spanning indexing, review, consensus, context building,
QA, HyDE, impact analysis, and arch summaries. This migration plan splits it into focused subsystems.

### Target Directory Structure

```
internal/rag/
├── service.go            # thin orchestrator + Service interface (wires subsystems)
├── cache.go              # ttlCache (already added)
│
├── index/
│   ├── indexer.go        # SetupRepoContext, UpdateRepoContext, ProcessFile
│   ├── filter.go         # filterFilesByExtensions, filterFilesByDirectories, etc.
│   └── hash.go           # computeFileHash, isTestFile, isLogicFile
│
├── review/
│   ├── review.go         # GenerateReview, GenerateReReview
│   ├── consensus.go      # GenerateConsensusReview, synthesizeConsensus, consensusMap/Reduce
│   ├── parser.go         # structuredReviewParser, ParseDiff, SanitizeModelForFilename
│   └── artifact.go       # saveReviewArtifact, saveConsensusArtifact, ensureReviewsDir
│
├── context/
│   ├── builder.go        # buildRelevantContext, buildContextConcurrently, assembleContext
│   ├── symbols.go        # gatherDefinitionsContext, resolveSymbolsConcurrently, extractSymbolsFromPatch
│   ├── impact.go         # getImpactDocs, buildImpactRequests, fetchImpactResults
│   ├── hyde.go           # gatherHyDEContext, generateHyDESnippet, stripPatchNoise, preFilterBM25
│   ├── arch.go           # getArchContext, GenerateArchSummaries, scanDirectoryOnDisk
│   └── format.go         # buildContextForPrompt, mergeChunksForFile, getDocKey, getDocContent
│
├── detect/
│   ├── reuse.go          # ReuseDetector (already self-contained — just move)
│   └── validator.go      # snippetValidator (already self-contained — just move)
│
└── question/
    └── qa.go             # AnswerQuestion, answerWithValidation/WithoutValidation
```

### Key Interface: `ContextBuilder`

The core decoupling point — review doesn't need to own context building, just consume it:

```go
// context/builder.go
type ContextBuilder interface {
    BuildContext(ctx context.Context, repo *storage.Repository,
        files []github.ChangedFile, description string) (context, definitions string)
}
```

```go
// review/review.go
type ReviewService struct {
    llm            llms.Model
    promptMgr      *llm.PromptManager
    contextBuilder context.ContextBuilder  // injected
    logger         *slog.Logger
}
```

### Dependency Map

| Subsystem      | Owns                              | Depends on                      |
|----------------|-----------------------------------|---------------------------------|
| `index/`       | Ingestion, chunking, hashing      | `storage`, `parsers`, `splitter`|
| `context/`     | 5-stage context pipeline          | `vectorStore`, `llms`, `packer` |
| `review/`      | Review generation + consensus     | `context.ContextBuilder`, `llms`|
| `detect/`      | Reuse detection, snippet validation| `vectorStore`, `llms`          |
| `question/`    | QA chain                          | `vectorStore`, `llms`           |
| `service.go`   | Wires subsystems, owns `Service`  | All of the above                |

### Migration Steps (No Big-Bang Rewrite)

Each step is a standalone PR that compiles and passes tests.

#### Phase 1: Extract `context/` (biggest win — 1000+ lines)
- [ ] Create `internal/rag/context/` package
- [ ] Move `buildRelevantContext`, `buildContextConcurrently`, `assembleContext`, `buildContextDocuments`, `fallbackConcat` → `context/builder.go`
- [ ] Move `gatherDefinitionsContext`, `resolveSymbolsConcurrently`, `extractSymbolsFromPatch`, `extractDepth0Symbols`, `resolveDepth2Symbols` → `context/symbols.go`
- [ ] Move `getImpactDocs`, `buildImpactRequests`, `fetchImpactResults` → `context/impact.go`
- [ ] Move `gatherHyDEContext`, `generateHyDESnippet`, `stripPatchNoise`, `preFilterBM25` → `context/hyde.go`
- [ ] Move `getArchContext`, `GenerateArchSummaries`, `scanDirectoryOnDisk`, `validateAndJoinPath` → `context/arch.go`
- [ ] Move `buildContextForPrompt`, `mergeChunksForFile`, `getDocKey`, `getDocContent` → `context/format.go`
- [ ] Define `ContextBuilder` interface, make `ragService.GenerateReview` call it
- [ ] Verify: `make lint && make test`

#### Phase 2: ~~Move `detect/` and `question/`~~ ✅
### ~~Extract `detect/` Package~~ ✅
- [x] Move `ReuseDetector` + types → `internal/rag/detect/reuse.go`
- [x] Move `snippetValidator` → `internal/rag/detect/validator.go`
- [x] Easy first step: they are completely self-contained, no cyclic dependencies

### ~~Extract `question/` Package~~ ✅
- [x] Move `AnswerQuestion`, `answerWithValidation`, `answerWithoutValidation` → `internal/rag/question/qa.go`
- [x] Update imports in callers
- [x] Verify: `make lint && make test`

#### Phase 3: ~~Extract `index/`~~ ✅
- [x] Move `SetupRepoContext`, `UpdateRepoContext`, `ProcessFile` → `index/indexer.go`
- [x] Move `filterFilesByExtensions`, `filterFilesByDirectories`, `filterFilesBySpecificFiles`, `filterFilesByValidExtensions`, `buildExcludeDirs` → `index/filter.go`
- [x] Move `computeFileHash`, `isTestFile`, `isLogicFile` → `index/hash.go`
- [x] Verify: `make lint && make test`

#### Phase 4: ~~Extract `review/`~~ ✅
- [x] Move `GenerateReview`, `GenerateReReview` → `review/review.go`
- [x] Move `GenerateConsensusReview`, `synthesizeConsensus`, consensus map/reduce funcs → `review/consensus.go`
- [x] Move `ParseDiff`, `structuredReviewParser`, `SanitizeModelForFilename` → `review/parser.go`
- [x] Move `saveReviewArtifact`, `saveConsensusArtifact`, `ensureReviewsDir` → `review/artifact.go`
- [x] Verify: `make lint && make test`

#### Phase 5: ~~Slim down `service.go`~~ ✅
- [x] `ragService` becomes a thin factory wiring all subsystems
- [x] `NewService` creates subsystems and composes them
- [x] `Service` interface delegates to subsystem methods
- [x] Verify: `make lint && make test`

### Add Metrics/Observability
- [ ] Track: context build time, LLM call duration, cache hit rates, symbol resolution depth
- [ ] Structured logging with consistent stage start/complete/skip patterns (partially done)

### Improve `ParseDiff` Robustness
- [ ] Handle binary file diffs (`Binary files differ`)
- [ ] Handle rename/move diffs (`rename from/to`)
- [ ] Add `Status` field (added/modified/deleted) based on diff header
