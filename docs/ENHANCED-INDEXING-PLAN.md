# Enhanced Code Indexing Plan

## Problem Statement

The MCP tools (`search_code`, `get_symbol`, `get_arch_context`) provide semantic search over code, but the ingestion pipeline only creates:
1. Generic code chunks (no `chunk_type`)
2. Architecture summaries (`chunk_type: "arch"`)

The `get_symbol` tool queries `chunk_type: "definition"` which doesn't exist, making it ineffective.

## Proposed Enhancements

### 1. Definition Chunks (`chunk_type: "definition"`)

**Purpose**: Enable fast lookup of type definitions, function signatures, and interfaces.

**Data Source**: During file processing, extract:
- Struct/interface definitions with field/method signatures
- Function definitions with full signatures
- Type aliases and constants (exported only)

**Metadata to include**:
```go
{
    "chunk_type": "definition",
    "source": "internal/rag/service.go",
    "line": 34,                    // Start line
    "end_line": 47,                 // End line
    "identifier": "Service",        // Primary name
    "kind": "interface",             // interface|struct|func|type|const
    "parent_id": "",                 // Parent identifier for nested types
    "package_name": "rag",
    "is_exported": true,
    "signature": "type Service interface { ... }", // Full definition
    "doc_summary": "Service is the main RAG pipeline interface...", // First doc line
}
```

**Extraction approach**:
- Use existing `ParserRegistry` from goframe
- Fallback to regex for unsupported languages
- Store alongside code chunks during `ProcessFile()`

**Implementation location**: `internal/rag/index/indexer.go`

---

### 2. Symbol Export Chunks (`chunk_type: "export"`)

**Purpose**: Quick lookup of a module's public API surface.

**Data Source**: Extract exported symbols per file:
- Exported functions with parameter names and types
- Exported types with field/method names
- Exported constants and variables

**Metadata to include**:
```go
{
    "chunk_type": "export",
    "source": "internal/rag/service.go",
    "package_name": "rag",
    "exports": ["NewService", "Service", "Config", "AnswerQuestion", ...],
    "export_summary": "type Service interface with methods: SetupRepoContext, UpdateRepoContext, ...",
}
```

**MCP Tool**: New tool `get_exports(package)` → Returns public API surface

---

### 3. Call Graph Metadata (`chunk_type: "callgraph"`)

**Purpose**: Understand dependencies and impact of changes.

**Data Source**: Extract import relationships and function calls:
- What functions/types a file imports
- What functions are called within a file

**Metadata to include**:
```go
{
    "chunk_type": "callgraph",
    "source": "internal/rag/service.go",
    "imports": ["context", "fmt", "github.com/sevigo/goframe/..."],
    "calls": ["NewBuilder", "NewService", "indexpkg.New"],
    "called_by": ["prescan/scanner.go", "jobs/review.go"], // Files that import this
}
```

**MCP Tool**: `find_dependencies(file)` and `find_dependents(file)`

---

### 4. Documentation Chunks (`chunk_type: "doc"`)

**Purpose**: Search documentation and comments separately from code.

**Data Source**: Extract:
- Package-level documentation (doc.go, package comments)
- Function/method documentation
- Important comment blocks (TODO, FIXME, NOTE)

**Metadata to include**:
```go
{
    "chunk_type": "doc",
    "source": "internal/rag/service.go",
    "line": 30,
    "doc_type": "function", // package|function|type|comment
    "identifier": "NewService",
    "content": "// NewService creates and returns a new RAG Service...",
}
```

---

### 5. Enhanced Code Chunks (Keep existing, add metadata)

**Current**: Code is split into chunks with `source` and sparse vectors.

**Enhancement**: Add more metadata for better filtering:
```go
{
    "chunk_type": "code",         // Explicit now
    "source": "internal/rag/service.go",
    "line": 132,
    "end_line": 145,
    "language": "go",             // File extension -> language
    "is_test": false,             // Already present
    "identifier": "NewService",   // Primary symbol in chunk (if any)
    "symbols": ["NewService", "Service", "Config"], // All symbols in chunk
    "complexity": 3,              // Cyclomatic complexity (optional)
}
```

---

## Implementation Plan

### Phase 1: Definition Extraction (Priority: High)

**Files to modify**:
1. `internal/rag/index/indexer.go` - Add `extractDefinitions()` function
2. `internal/rag/index/definitions.go` - New file for definition extraction logic

**Approach**:
```go
// In ProcessFile()
func (i *Indexer) ProcessFile(ctx context.Context, repoPath, file string) []schema.Document {
    // ... existing code chunking ...
    
    // NEW: Extract definitions
    defDocs := i.extractDefinitions(fullPath, file, splitDocs)
    allDocs = append(allDocs, defDocs...)
    
    return allDocs
}
```

**Parser-based extraction** (using existing goframe parsers):
```go
func (i *Indexer) extractDefinitions(fullPath, file string, chunks []schema.Document) []schema.Document {
    ext := filepath.Ext(fullPath)
    parser, err := i.cfg.ParserRegistry.GetParserForExtension(ext)
    if err != nil {
        return i.extractDefinitionsRegex(fullPath, file, chunks)
    }
    
    definitions := parser.ExtractDefinitions(content)
    // Convert to documents with chunk_type="definition"
}
```

### Phase 2: Update MCP Tools

**`internal/mcp/tools/get_symbol.go`** - Keep existing logic, it will now find results:
```go
// Query already correct:
vectorstores.WithFilters(map[string]any{
    "chunk_type": "definition",
})
```

**`internal/mcp/tools/search_code.go`** - Add chunk_type parameter already exists, just needs to work:
```go
// Already supports chunk_type filtering
// Will now return definition chunks when chunk_type="definition"
```

### Phase 3: Export API Surface (Priority: Medium)

**New file**: `internal/rag/index/exports.go`

**New MCP tool**: `internal/mcp/tools/get_exports.go`
```go
func (t *GetExports) Execute(ctx context.Context, args map[string]any) (any, error) {
    packageName := args["package"].(string) // e.g., "internal/rag"
    
    results, err := t.VectorStore.SimilaritySearch(ctx, 
        fmt.Sprintf("exports API surface %s", packageName), 5,
        vectorstores.WithFilters(map[string]any{
            "chunk_type": "export",
            "source": packageName,
        }),
    )
    // ...
}
```

### Phase 4: Call Graph (Priority: Low)

**New file**: `internal/rag/index/callgraph.go`

**New MCP tools**: 
- `get_dependencies(file)` - What does this file need?
- `get_dependents(file)` - What files use this?

---

## Testing Strategy

### Unit Tests
1. `internal/rag/index/definitions_test.go` - Test extraction for Go, TypeScript, Python, etc.
2. `internal/rag/index/exports_test.go` - Test export surface generation

### Integration Test
1. Ingest a sample repository
2. Query `get_symbol("Service")` - Should return definition
3. Query `get_exports("internal/rag")` - Should return public API
4. Query `search_code("RAG pipeline", chunk_type="code")` - Should return code chunks
5. Query `search_code("interface definition", chunk_type="definition")` - Should return definitions

---

## Success Metrics

| Metric | Current | Target |
|--------|---------|--------|
| `get_symbol` effective results | ~0% (empty) | >80% for exported symbols |
| Definition chunks per repo | 0 | 100-1000 (depends on size) |
| Time to find `Service` interface | N/A | <100ms via vector search |
| Context precision for reviews | Good | Better (explicit definitions included) |

---

## Questions to Resolve

1. **Storage overhead**: Definition chunks will duplicate some code content. Acceptable trade-off?

2. **Language support**: Start with Go-only definitions, or implement for all supported languages?

3. **Incremental updates**: Definition extraction on `UpdateRepoContext` for changed files only?

4. **Chunk size limits**: Full struct definitions can be large. Set a max size?

---

## Implementation Order

1. ✅ Create branch `feature/enhanced-code-indexing`
2. ⬜ Implement `internal/rag/index/definitions.go` with Go support
3. ⬜ Update `indexer.go` to call definition extraction
4. ⬜ Write unit tests for definition extraction
5. ⬜ Test with existing `get_symbol` tool
6. ⬜ Add TypeScript/JavaScript definition extraction
7. ⬜ Add Python definition extraction
8. ⬜ Implement export API surface (Phase 2)
9. ⬜ Implement call graph (Phase 3 - optional)