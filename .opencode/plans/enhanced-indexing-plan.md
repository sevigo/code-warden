# Enhanced Code Indexing Plan - Comprehensive Implementation

## Overview

Implement all 5 indexing features in a single pass to provide comprehensive code discovery for MCP tools and code review pipeline.

## Current State Analysis

### What's Indexed Now

| Chunk Type | When Created | Metadata | Used By |
|------------|--------------|----------|---------|
| Code chunks | `ProcessFile()` | `source`, `is_test`, `Sparse` | `search_code`, HyDE, impact |
| Architecture | `GenerateArchSummaries()` | `chunk_type: "arch"`, `source` (dir) | `get_arch_context`, `get_structure` |

### What's Missing (All 5 Features)

1. **Definition chunks** - `get_symbol` returns empty (queries non-existent `chunk_type: "definition"`)
2. **Usage/reference index** - Cannot find "where is this called?"
3. **Call graph edges** - Cannot trace blast radius
4. **Test coverage links** - Cannot warn about untested code
5. **Error signatures** - Not included (lower priority)

---

## Sprint 1: Definition Chunks (CRITICAL)

### Problem
`get_symbol` tool queries `chunk_type: "definition"` which doesn't exist.

### Solution
Extract and index type/func/interface definitions during `ProcessFile()`.

### Files to Create/Modify

**New file: `internal/rag/index/definitions.go`**

```go
package index

// DefinitionExtractor extracts type/func/interface definitions from source files.
type DefinitionExtractor struct {
    parserRegistry parsers.ParserRegistry
    logger         *slog.Logger
}

// Definition represents a code definition to be indexed.
type Definition struct {
    Identifier   string // "Service", "NewService", "Config"
    Kind         string // "interface", "struct", "func", "type", "const", "var"
    Signature    string // Full definition text
    Line         int    // Start line
    EndLine      int    // End line
    PackageName  string
    IsExported   bool
    ParentID     string // For nested types
    DocSummary   string // First line of doc comment
}

// ExtractDefinitions extracts definitions from a file.
func (d *DefinitionExtractor) ExtractDefinitions(ctx context.Context, fullPath, relPath string, content []byte) []schema.Document
```

**Modify: `internal/rag/index/indexer.go`**

```go
func (i *Indexer) ProcessFile(ctx context.Context, repoPath, file string) []schema.Document {
    // ... existing code chunking ...
    
    // NEW: Extract definitions
    defExtractor := NewDefinitionExtractor(i.cfg.ParserRegistry, i.cfg.Logger)
    defDocs := defExtractor.ExtractDefinitions(ctx, fullPath, file, contentBytes)
    
    // NEW: Add chunk_type to code chunks (explicitly)
    for j := range splitDocs {
        splitDocs[j].Metadata["chunk_type"] = "code"
        splitDocs[j].Metadata["language"] = filepath.Ext(file)
    }
    
    return append(splitDocs, defDocs...)
}
```

### Language Support (Parser First, Regex Fallback)

| Language | Parser | Regex Patterns |
|----------|--------|----------------|
| Go | goframe | struct, interface, func, type, const |
| TypeScript | goframe | class, interface, type, function |
| JavaScript | goframe | class, function, const |
| Python | goframe | class, def |
| Java | - | class, interface, method |
| Rust | - | struct, enum, trait, fn |

### Document Schema

```json
{
  "PageContent": "type Service interface {\n    SetupRepoContext(...)\n}",
  "Metadata": {
    "chunk_type": "definition",
    "source": "internal/rag/service.go",
    "line": 34,
    "end_line": 47,
    "identifier": "Service",
    "kind": "interface",
    "package_name": "rag",
    "is_exported": true
  },
  "Sparse": "..."
}
```

### Tests
- `internal/rag/index/definitions_test.go`

---

## Sprint 2: Usage/Reference Index

### Problem
Cannot find "where is this function called?" or "all implementations of this interface".

### Solution
Index call sites and references during file processing.

### Files to Create/Modify

**New file: `internal/rag/index/references.go`**

```go
// ReferenceExtractor extracts function calls and type references.
type ReferenceExtractor struct {
    parserRegistry parsers.ParserRegistry
    logger         *slog.Logger
}

// ExtractReferences extracts all references from a file.
func (r *ReferenceExtractor) ExtractReferences(ctx context.Context, fullPath, relPath string, content []byte) []schema.Document
```

### New MCP Tool: `find_usages`

**New file: `internal/mcp/tools/find_usages.go`**

```go
type FindUsages struct {
    VectorStore storage.ScopedVectorStore
    Logger      *slog.Logger
}

// Execute searches for usages of a symbol.
// args: {"symbol": "ProcessFile", "kind": "call|implement|import"}
func (t *FindUsages) Execute(ctx context.Context, args map[string]any) (any, error) {
    symbol := args["symbol"].(string)
    kind := args["kind"] // optional: "call", "implement", "import"
    
    // Query: symbol + kind filter
    results := t.VectorStore.SimilaritySearch(ctx, 
        fmt.Sprintf("usage call reference %s", symbol), 50,
        vectorstores.WithFilters(map[string]any{
            "chunk_type": "reference",
        }),
    )
    // Filter by identifier and kind
}
```

### Document Schema

```json
{
  "PageContent": "ProcessFile(ctx, repoPath, file)",
  "Metadata": {
    "chunk_type": "reference",
    "source": "internal/jobs/review.go",
    "line": 120,
    "identifier": "ProcessFile",
    "kind": "call",
    "caller_function": "GenerateReview",
    "resolved_to": "internal/rag/index/indexer.go:428"
  }
}
```

---

## Sprint 3: Call Graph Edges

### Problem
Cannot understand blast radius - "what does this function call?"

### Solution
Index call graph relationships per file.

### Files to Create/Modify

**New file: `internal/rag/index/callgraph.go`**

```go
// CallGraph represents the call relationships in a file.
type CallGraph struct {
    Source      string
    Functions   []FuncNode
    Imports     []string
}

type FuncNode struct {
    Name       string
    Line       int
    Calls      []string
    IsExported bool
}

func ExtractCallGraph(fullPath, relPath string, content []byte) *CallGraph
```

### New MCP Tools

**New files:**
- `internal/mcp/tools/get_callers.go` - Who calls this function?
- `internal/mcp/tools/get_callees.go` - What does this function call?

### Document Schema

```json
{
  "PageContent": "SetupRepoContext calls: indexer.SetupRepoContext, GenerateArchSummaries",
  "Metadata": {
    "chunk_type": "callgraph",
    "source": "internal/rag/service.go",
    "function": "SetupRepoContext",
    "line": 346,
    "calls": ["indexer.SetupRepoContext", "GenerateArchSummaries"],
    "is_exported": true
  }
}
```

---

## Sprint 4: Test Coverage Links

### Problem
Cannot detect "this changed function has no tests".

### Solution
Index test-to-source links during ingestion.

### Files to Create/Modify

**New file: `internal/rag/index/testlinks.go`**

```go
// TestLinkExtractor extracts links between test and source files.
type TestLinkExtractor struct {
    logger *slog.Logger
}

func (t *TestLinkExtractor) ExtractTestLinks(repoPath string, testFiles, sourceFiles []string) []TestLink
```

### Review Enhancement

```go
// In review pipeline:
for _, changedFunc := range changedFunctions {
    coverage := FindTestCoverage(changedFunc)
    if len(coverage.Tests) == 0 {
        warnings = append(warnings, 
            "Function %s modified but has no test coverage", changedFunc)
    }
}
```

### Document Schema

```json
{
  "PageContent": "service_test.go tests: SetupRepoContext, GenerateReview",
  "Metadata": {
    "chunk_type": "test_link",
    "source": "internal/rag/service_test.go",
    "tested_file": "internal/rag/service.go",
    "tested_functions": ["SetupRepoContext", "GenerateReview"],
    "test_type": "unit"
  }
}
```

---

## Sprint 5: Enhanced Code Chunks

### Enhancement
Add richer metadata to existing code chunks.

### Changes to `ProcessFile()`

```go
for i := range splitDocs {
    splitDocs[i].Metadata["chunk_type"] = "code"
    splitDocs[i].Metadata["language"] = filepath.Ext(file)
    splitDocs[i].Metadata["line"] = computeLineNumber(...)
    splitDocs[i].Metadata["end_line"] = splitDocs[i].Metadata["line"].(int) + countLines(...)
    
    // Extract symbols in this chunk
    symbols := extractSymbolsFromChunk(splitDocs[i].PageContent)
    if len(symbols) > 0 {
        splitDocs[i].Metadata["identifier"] = symbols[0]
        splitDocs[i].Metadata["symbols"] = symbols
    }
}
```

---

## Implementation Timeline

| Sprint | Files | Effort | Dependencies |
|--------|-------|--------|--------------|
| 1. Definitions | definitions.go, indexer.go, definitions_test.go, get_symbol.go | 2-3 days | None |
| 2. References | references.go, find_usages.go, references_test.go | 2-3 days | Sprint 1 |
| 3. Call Graph | callgraph.go, get_callers.go, get_callees.go, callgraph_test.go | 2-3 days | Sprint 2 |
| 4. Test Links | testlinks.go, testlinks_test.go, review/service.go | 1-2 days | Sprint 2 |
| 5. Code Chunks | indexer.go | 1 day | Sprint 1 |

**Total: 8-12 days**

---

## Storage Overhead

| Chunk Type | Docs/File | Size Overhead |
|------------|-----------|---------------|
| Definitions | 3-10 | +10-30% |
| References | 10-50 | +20-50% |
| Call Graph | 1/func | +5-10% |
| Test Links | 0-1 | Negligible |
| **Total** | | **+35-90%** |

---

## Success Criteria

| Metric | Before | After |
|--------|--------|-------|
| `get_symbol("Service")` | Empty | Returns interface definition |
| `find_usages("ProcessFile")` | N/A | Returns call sites |
| Context in reviews | Good | Better (explicit definitions) |
| "No tests" warning | N/A | Works |

---

## Validation Commands

```bash
# After Phase 1:
warden-cli prescan <repo>
warden-cli mcp query 'get_symbol("Service")'
# Should return: {identifier: "Service", kind: "interface", ...}

# After Phase 2:
warden-cli mcp query 'find_usages("ProcessFile")'
# Should return: [{file: "review.go", line: 120, kind: "call"}, ...]

# After Phase 3:
warden-cli mcp query 'get_callers("GenerateReview")'
# Should return: [{file: "review.go", function: "runReview"}, ...]
```

---

## Open Questions

1. **Max definition size?** Truncate at 50 lines or include full?

2. **Reference depth limit?** 50 references max per query?

3. **Include external dependencies in call graph?** Currently stdlib excluded.

4. **Language priority?** Go first, then TypeScript/JavaScript/Python?