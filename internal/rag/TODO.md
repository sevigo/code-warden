# RAG Package TODO

## High Priority

### Fix `SetupRepoContext` Worker Pool Shutdown
- [ ] Refactor `Indexer` to expose or better control the worker pool.
- [ ] Add unit test to verify graceful shutdown on context cancellation.

### Improve `ParseDiff` Robustness
- [ ] Handle binary file diffs (`Binary files differ`).
- [ ] Handle rename/move diffs (`rename from/to`).
- [ ] Add `Status` field (added/modified/deleted) based on diff header.

## Medium Priority

### Metrics and Observability
- [ ] Track context build time per stage (HyDE, Symbols, Impact, etc.).
- [ ] Track LLM call durations and token usage in QA and Review.
- [ ] Implement cache hit/miss metrics for `ttlCache`.
- [ ] Standardize structured logging across all subsystems.

### Parse `getConsensusTimeout` Once at Construction
- [ ] Move `time.ParseDuration` to `NewService` or subsystem constructors.
- [ ] Store parsed duration in the service/subsystem structs.

## Low Priority

### Data Structure Optimizations
- [ ] Replace `containsString` with O(1) map lookups in `extractDocMetadata`.
- [ ] Convert `info.Files` and `info.Symbols` to sets/maps where applicable.

### SHA256 Hashing Helper
- [ ] Create a shared utility for `sha256.Sum256` pattern.
- [ ] Parameterize truncation length (e.g., 8 bytes for short IDs vs 16 bytes for file hashes).

## Completed Refactoring (Summary)
- [x] Extracted `contextpkg` (Builder, Symbols, HyDE, Impact, Arch, Format).
- [x] Extracted `index` (Indexer, Filter, Hash).
- [x] Extracted `question` (QA Service, exported internal methods).
- [x] Extracted `review` (Review, Consensus, Parser, Artifact).
- [x] Extracted `detect` (Reuse, Validator).
- [x] Slimmed down `service.go` into a thin orchestrator.
- [x] Added comprehensive unit tests for `index` and `question` packages.
