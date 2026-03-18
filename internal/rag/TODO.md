# RAG Package TODO

## High Priority

### Complete Definition Chunk Indexing (Unblocks `get_symbol` MCP Tool)
The `get_symbol` MCP tool currently returns empty results because definition chunks are not
yet being stored during indexing. This makes the tool useless and forces agents to fall back
to less accurate vector search for symbol lookups.
- [ ] Complete `definitions.go` extractor: emit `chunk_type: "definition"` chunks for Go types, funcs, interfaces, consts
- [ ] Wire extractor into the indexing pipeline alongside code chunks (see `SetupRepoContext`)
- [ ] Add metadata fields: `identifier`, `kind` (type/func/interface/var/const), `package_name`, `line`
- [ ] Add integration test: index a known Go file, assert definition chunks exist in Qdrant
- [ ] Verify `get_symbol` returns correct results end-to-end after indexing

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
