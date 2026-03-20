package index

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sevigo/goframe/documentloaders"
	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/textsplitter"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/storage"
)

// Config holds dependencies for the Indexer.
type Config struct {
	Store          storage.Store
	VectorStore    storage.VectorStore
	ParserRegistry parsers.ParserRegistry
	Splitter       textsplitter.TextSplitter
	Logger         *slog.Logger
	EmbedderModel  string
}

// Indexer handles document ingestion and semantic chunking.
type Indexer struct {
	cfg Config
}

// New creates a new [Indexer] instance.
func New(cfg Config) *Indexer {
	return &Indexer{cfg: cfg}
}

// ProgressFunc is called periodically during indexing with the number of
// files processed so far and the total discovered so far (total grows as
// the file stream is consumed, so it may increase over time).
// Implementations must be safe to call from multiple goroutines.
type ProgressFunc func(done, total int)

// SetupRepoContext indexes a repository for the first time or re-indexes
// using smart scan (file-hash based skipping).
//
// Both SetupRepoContext and UpdateRepoContext deliberately share the same
// per-file logic by routing through ProcessFile.  This guarantees identical
// chunk quality, metadata, sparse vectors, and definition extraction on both
// the full-index and incremental-update paths.
//
//nolint:cyclop,gocyclo,gocognit,funlen // orchestrates complex smart-scan workflow
func (i *Indexer) SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, progressFn ProgressFunc) error {
	i.cfg.Logger.Info("performing smart indexing with GoFrame GitLoader",
		"path", repoPath,
		"collection", repo.QdrantCollectionName,
	)
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	finalExcludeDirs := BuildExcludeDirs(repoConfig)
	startTime := time.Now()

	// Count total files upfront for accurate progress reporting
	totalFiles := 0
	filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// Quick filter for common exclusions
		name := d.Name()
		if strings.HasPrefix(name, ".") && name != "." {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			totalFiles++
		}
		return nil
	})
	i.cfg.Logger.Info("counted files for indexing", "total", totalFiles)

	// Smart Scan: Fetch existing file states for fast skipping
	existingFiles, err := i.cfg.Store.GetFilesForRepo(ctx, repo.ID)
	if err != nil {
		i.cfg.Logger.Warn("failed to fetch existing file states", "error", err)
		existingFiles = make(map[string]storage.FileRecord)
	}

	// Copy existingFiles to avoid race condition (it's read-only after this point)
	existingFilesCopy := make(map[string]storage.FileRecord, len(existingFiles))
	for k, v := range existingFiles {
		existingFilesCopy[k] = v
	}

	// Initialize GoFrame's GitLoader for file discovery and filtering only.
	// The loader handles exclude dirs, exclude exts, binary detection, and
	// generated-code detection.  Actual chunking is delegated to ProcessFile
	// so both indexing paths produce identical documents.
	loader, err := documentloaders.NewGit(repoPath, i.cfg.ParserRegistry,
		documentloaders.WithExcludeDirs(finalExcludeDirs),
		documentloaders.WithExcludeExts(repoConfig.ExcludeExts),
		documentloaders.WithWorkerCount(4),
		documentloaders.WithGeneratedCodeDetection(true),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize git loader: %w", err)
	}

	scopedStore := i.cfg.VectorStore.ForRepo(repo.QdrantCollectionName, i.cfg.EmbedderModel)
	var processedCount int64 // atomic counter for progress
	var skippedCount int64   // atomic counter for progress
	var totalSeen int64      // atomically incremented as files are discovered

	// Keep track of all files processed by the loader to identify deletions later
	filesProcessedByLoader := make(map[string]struct{})
	var filesProcessedByLoaderMu sync.Mutex

	// Worker pool: hash-check then call ProcessFile (same as UpdateRepoContext path).
	const numHashWorkers = 4
	const batchSize = 500 // Limit memory usage

	// fileWork carries only the paths; ProcessFile reads the file from disk.
	type fileWork struct {
		file     string // relative path
		filePath string // absolute path
	}

	type fileResult struct {
		docsToInsert []schema.Document
		fileToUpdate storage.FileRecord
		processed    bool
		skipped      bool
		filePath     string // for progress reporting
	}

	// Use larger buffer to prevent pipeline deadlock
	fileChan := make(chan fileWork, numHashWorkers*2)
	resultChan := make(chan fileResult, numHashWorkers*2)

	// Batch accumulation for memory-bounded inserts
	var batchDocs []schema.Document
	var batchFiles []storage.FileRecord

	// Start worker pool
	var wg sync.WaitGroup
	for range numHashWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case work, ok := <-fileChan:
					if !ok {
						return
					}
					// Compute hash
					var hash string
					var hashErr error
					if work.filePath != "" {
						hash, hashErr = ComputeFileHash(work.filePath)
					}
					if hashErr != nil {
						i.cfg.Logger.Warn("hash failed, will re-process", "file", work.file, "error", hashErr)
					}

					// Skip unchanged files
					if hash != "" {
						if rec, exists := existingFilesCopy[work.file]; exists && rec.FileHash == hash {
							// Report progress for skipped file
							if progressFn != nil {
								done := int(atomic.LoadInt64(&processedCount) + atomic.LoadInt64(&skippedCount) + 1)
								progressFn(done, totalFiles)
							}
							atomic.AddInt64(&skippedCount, 1)
							resultChan <- fileResult{processed: true, skipped: true, filePath: work.file}
							continue
						}
					}

					// ProcessFile produces code chunks + definition chunks with the
					// exact same logic used by UpdateRepoContext, ensuring both paths
					// yield identical document quality.
					docs := i.ProcessFile(ctx, repoPath, work.file)

					fileRec := storage.FileRecord{}
					if hash != "" {
						fileRec = storage.FileRecord{
							RepositoryID: repo.ID,
							FilePath:     work.file,
							FileHash:     hash,
						}
					}

					resultChan <- fileResult{docsToInsert: docs, fileToUpdate: fileRec, processed: true, filePath: work.file}
					atomic.AddInt64(&processedCount, 1)
				}
			}
		}()
	}

	// Start result collector goroutine to prevent deadlock
	resultsMu := sync.Mutex{}

	const progressInterval = 10 // report every N files to avoid excessive DB writes

	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for res := range resultChan {
			resultsMu.Lock()
			// Accumulate for batch insert
			batchDocs = append(batchDocs, res.docsToInsert...)
			if res.fileToUpdate.FilePath != "" {
				batchFiles = append(batchFiles, res.fileToUpdate)
			}

			// Flush batch when full
			if len(batchDocs) >= batchSize {
				if _, err := scopedStore.AddDocuments(ctx, batchDocs); err != nil {
					i.cfg.Logger.Error("failed to add vectors in batch", "error", err)
				}
				if err := i.cfg.Store.UpsertFiles(ctx, repo.ID, batchFiles); err != nil {
					i.cfg.Logger.Error("failed to update file state in DB", "error", err)
				}
				// Clear batches but keep capacity
				batchDocs = batchDocs[:0]
				batchFiles = batchFiles[:0]
			}

			resultsMu.Unlock()

			// Report progress periodically to avoid hammering the caller
			done := int(atomic.LoadInt64(&processedCount) + atomic.LoadInt64(&skippedCount))
			if progressFn != nil && done%progressInterval == 0 {
				progressFn(done, int(atomic.LoadInt64(&totalSeen)))
			}
		}
	}()

	// Phase 1: Stream file discovery via GitLoader (filtering, binary detection, generated-code skip).
	// We only use source paths from the batch; ProcessFile handles all content processing.
	err = loader.LoadAndProcessStream(ctx, func(_ context.Context, docs []schema.Document) error {
		// Collect unique source paths from this batch.
		seen := make(map[string]struct{})
		for _, doc := range docs {
			source, _ := doc.Metadata["source"].(string)
			if source == "" {
				continue
			}
			if _, already := seen[source]; already {
				continue
			}
			seen[source] = struct{}{}

			atomic.AddInt64(&totalSeen, 1)

			filesProcessedByLoaderMu.Lock()
			filesProcessedByLoader[source] = struct{}{}
			filesProcessedByLoaderMu.Unlock()

			fullPath := filepath.Join(repoPath, source)
			select {
			case fileChan <- fileWork{file: source, filePath: fullPath}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})

	// Cleanup: close fileChan and wait for workers
	close(fileChan)
	wg.Wait()
	close(resultChan)
	<-collectorDone // Wait for collector to finish

	// Flush remaining batch (no mutex needed - collector goroutine has finished)
	if len(batchDocs) > 0 {
		if _, err := scopedStore.AddDocuments(ctx, batchDocs); err != nil {
			i.cfg.Logger.Error("failed to add vectors in final batch", "error", err)
		}
		if err := i.cfg.Store.UpsertFiles(ctx, repo.ID, batchFiles); err != nil {
			i.cfg.Logger.Error("failed to update file state in final DB batch", "error", err)
		}
	}

	// Cleanup: Delete records for files that are genuinely absent from disk AND were not processed by loader.
	// We check the filesystem directly rather than relying on filesProcessedByLoader alone,
	// but we respect filesProcessedByLoader as "exists" to avoid unnecessary stat calls.
	var pathsToDelete []string
	for path := range existingFiles {
		// Optimization: If loader processed it, it definitely exists.
		if _, seen := filesProcessedByLoader[path]; seen {
			continue
		}

		fullPath := filepath.Join(repoPath, path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			pathsToDelete = append(pathsToDelete, path)
		}
	}

	if len(pathsToDelete) > 0 {
		i.cfg.Logger.Info("pruning deleted files from tracking", "count", len(pathsToDelete))
		if err := i.cfg.Store.DeleteFiles(ctx, repo.ID, pathsToDelete); err != nil {
			i.cfg.Logger.Warn("failed to delete stale file records", "error", err)
		}
		// Also remove from Qdrant?
		// We assume Qdrant clean up is handled via re-indexing or manual pruned?
		// Actually `processFilesParallel` handles UPSERT.
		// Deleting from Qdrant requires `DeleteDocumentsByFilter` ("source" in pathsToDelete).
		if len(pathsToDelete) > 0 && repo.QdrantCollectionName != "" {
			if err := i.cfg.VectorStore.DeleteDocumentsFromCollectionByFilter(ctx, repo.QdrantCollectionName, i.cfg.EmbedderModel, map[string]any{"source": map[string]any{"$in": pathsToDelete}}); err != nil {
				i.cfg.Logger.Warn("failed to delete vectors for removed files", "error", err)
			}
		}
	}

	i.cfg.Logger.Info("repository setup complete",
		"indexed_files", processedCount,
		"skipped_files", skippedCount,
		"duration", time.Since(startTime).Round(time.Second),
	)

	return nil
}

// UpdateRepoContext incrementally updates the vector store for changed files.
//
//nolint:gocognit,nestif,funlen // incremental sync has inherently complex control flow
func (i *Indexer) UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string) error {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	// Get the same exclude directories configuration as SetupRepoContext
	finalExcludeDirs := BuildExcludeDirs(repoConfig)

	// Apply directory filtering first, then extension filtering, then specific file filtering
	filesToProcess = FilterFilesByDirectories(filesToProcess, finalExcludeDirs)
	filesToDelete = FilterFilesByDirectories(filesToDelete, finalExcludeDirs)

	// Apply valid extension whitelist (same as scanner)
	filesToProcess = FilterFilesByValidExtensions(filesToProcess)
	filesToDelete = FilterFilesByValidExtensions(filesToDelete)

	filesToProcess = FilterFilesByExtensions(filesToProcess, repoConfig.ExcludeExts)
	filesToDelete = FilterFilesByExtensions(filesToDelete, repoConfig.ExcludeExts)

	filesToProcess = FilterFilesBySpecificFiles(filesToProcess, repoConfig.ExcludeFiles)
	filesToDelete = FilterFilesBySpecificFiles(filesToDelete, repoConfig.ExcludeFiles)

	i.cfg.Logger.Info("updating repository context after filtering",
		"collection", repo.QdrantCollectionName,
		"process", len(filesToProcess),
		"delete", len(filesToDelete),
		"exclude_dirs", finalExcludeDirs,
		"exclude_exts", repoConfig.ExcludeExts,
		"exclude_files", repoConfig.ExcludeFiles,
	)

	// Handle deleted files first
	if len(filesToDelete) > 0 {
		i.cfg.Logger.Info("deleting embeddings for removed files", "count", len(filesToDelete))
		if err := i.cfg.VectorStore.DeleteDocumentsFromCollection(ctx, repo.QdrantCollectionName, i.cfg.EmbedderModel, filesToDelete); err != nil {
			i.cfg.Logger.Error("failed to delete some embeddings", "error", err)
		}
	}

	// Handle added and modified files
	if len(filesToProcess) == 0 {
		return nil
	}

	// Process files in parallel using a worker pool
	type fileResult struct {
		docs []schema.Document
	}

	const numWorkers = 4
	fileChan := make(chan string, len(filesToProcess))
	resultChan := make(chan fileResult, len(filesToProcess))

	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range fileChan {
				docs := i.ProcessFile(ctx, repoPath, f)
				resultChan <- fileResult{docs: docs}
			}
		}()
	}

	for _, f := range filesToProcess {
		fileChan <- f
	}
	close(fileChan)

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// avgChunksPerFile is based on observed average file sizes and chunking strategy (~4 chunks/file).
	// Adjust if profiling reveals consistent over/under-allocation.
	const avgChunksPerFile = 4

	// Pre-allocate with an estimated capacity to reduce GC pressure during indexing.
	allDocs := make([]schema.Document, 0, len(filesToProcess)*avgChunksPerFile)
	for res := range resultChan {
		allDocs = append(allDocs, res.docs...)
	}

	if len(allDocs) > 0 {
		i.cfg.Logger.Info("adding/updating documents in vector store", "count", len(allDocs))
		scopedStore := i.cfg.VectorStore.ForRepo(repo.QdrantCollectionName, i.cfg.EmbedderModel)
		if _, err := scopedStore.AddDocuments(ctx, allDocs); err != nil {
			return fmt.Errorf("failed to add/update embeddings for changed files: %w", err)
		}

		// Update file hashes so smart-scan can skip these files next time.
		var fileRecords []storage.FileRecord
		for _, f := range filesToProcess {
			fullPath := filepath.Join(repoPath, f)
			hash, err := ComputeFileHash(fullPath)
			if err != nil {
				i.cfg.Logger.Warn("failed to hash file for tracking", "file", f, "error", err)
				continue
			}
			fileRecords = append(fileRecords, storage.FileRecord{
				RepositoryID: repo.ID,
				FilePath:     f,
				FileHash:     hash,
			})
		}
		if len(fileRecords) > 0 {
			if err := i.cfg.Store.UpsertFiles(ctx, repo.ID, fileRecords); err != nil {
				i.cfg.Logger.Warn("failed to update file hashes in DB", "error", err)
			}
		}
	}

	return nil
}

// ProcessFile reads, parses, and chunks a single file for indexing.
// Returns code chunks and definition chunks.
//
//nolint:funlen,gocognit
func (i *Indexer) ProcessFile(ctx context.Context, repoPath, file string) []schema.Document {
	fullPath := filepath.Join(repoPath, file)

	// Read file for chunking
	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		i.cfg.Logger.Error("failed to read file for processing", "file", file, "error", err)
		return nil
	}

	// Ensure valid UTF-8 and create a document for the splitter.
	validContent := strings.ToValidUTF8(string(contentBytes), "")
	doc := schema.NewDocument(validContent, map[string]any{
		"source": file,
	})

	splitDocs, err := i.cfg.Splitter.SplitDocuments(ctx, []schema.Document{doc})
	if err != nil {
		i.cfg.Logger.Error("failed to split document with code-aware splitter", "file", file, "error", err)
		return nil
	}

	ext := strings.ToLower(filepath.Ext(file))

	// Build line offset map for computing line numbers
	lineOffsets := buildLineOffsets(validContent)

	// Filter boilerplate chunks (import blocks, package-only lines, etc.) before
	// processing so they don't occupy vector-store slots or dilute search results.
	filtered := splitDocs[:0]
	for _, chunk := range splitDocs {
		if isLikelyBoilerplate(chunk.PageContent) {
			i.cfg.Logger.Debug("skipping boilerplate chunk", "file", file)
			continue
		}
		filtered = append(filtered, chunk)
	}
	splitDocs = filtered

	for idx := range splitDocs {
		// Ensure sparse vectors are generated for hybrid search if possible
		sparseVec, err := sparse.GenerateSparseVector(ctx, splitDocs[idx].PageContent)
		if err == nil {
			splitDocs[idx].Sparse = sparseVec
		} else {
			i.cfg.Logger.Debug("sparse vector generation failed for chunk, using dense only", "file", file, "chunk", idx, "error", err)
		}

		// Set chunk_type explicitly for code chunks
		splitDocs[idx].Metadata["chunk_type"] = "code"
		splitDocs[idx].Metadata["language"] = ext

		// Compute line numbers
		if line, endLine, ok := findLineNumbers(validContent, splitDocs[idx].PageContent, lineOffsets); ok {
			splitDocs[idx].Metadata["line"] = line
			splitDocs[idx].Metadata["end_line"] = endLine
		}

		// Extract symbols from chunk.
		// Prefer the parser's AST-aware ExtractUsedSymbols which understands
		// language syntax and avoids noise like keywords and common variable names.
		// Fall back to regex only when no parser is available for this extension.
		var symbols []string
		usedParser := false
		if i.cfg.ParserRegistry != nil {
			if parser, parserErr := i.cfg.ParserRegistry.GetParserForExtension(ext); parserErr == nil {
				symbols = parser.ExtractUsedSymbols(splitDocs[idx].PageContent)
				usedParser = len(symbols) > 0
			}
		}
		if len(symbols) == 0 {
			symbols = extractSymbolsFromChunk(splitDocs[idx].PageContent, ext)
		}
		i.cfg.Logger.Debug("symbol extraction complete", "file", file, "chunk", idx, "symbols", len(symbols), "parser", usedParser)
		if len(symbols) > 0 {
			splitDocs[idx].Metadata["symbols"] = symbols
			// Primary symbol is the first exported one
			for _, sym := range symbols {
				if len(sym) > 0 && sym[0] >= 'A' && sym[0] <= 'Z' {
					splitDocs[idx].Metadata["identifier"] = sym
					break
				}
			}
		}

		// Polyfill: Ensure is_test is set based on filename
		if IsTestFile(file) {
			splitDocs[idx].Metadata["is_test"] = true

			// Extract tested symbols for test-to-code linkage
			testedSymbols := ExtractTestedSymbols(file, splitDocs[idx].PageContent)
			if len(testedSymbols) > 0 {
				symbolNames := make([]string, 0, len(testedSymbols))
				for _, ts := range testedSymbols {
					symbolNames = append(symbolNames, ts.Symbol)
				}
				splitDocs[idx].Metadata["tested_symbols"] = symbolNames
				splitDocs[idx].Metadata["source_file"] = InferSourceFile(file)
			}
		}
	}

	// Extract definitions from the file
	var allDocs []schema.Document
	allDocs = append(allDocs, splitDocs...)

	if i.cfg.ParserRegistry != nil {
		defExtractor := NewDefinitionExtractor(i.cfg.ParserRegistry, i.cfg.Logger)
		defDocs := defExtractor.ExtractDefinitions(ctx, fullPath, file, contentBytes)

		// Generate sparse vectors for definition chunks
		for idx := range defDocs {
			sparseVec, err := sparse.GenerateSparseVector(ctx, defDocs[idx].PageContent)
			if err == nil {
				defDocs[idx].Sparse = sparseVec
			}
		}

		allDocs = append(allDocs, defDocs...)
		allDocs = append(allDocs, i.buildTOCDocs(ctx, file, defDocs)...)
	}

	return allDocs
}

// buildTOCDocs creates a file-level TOC chunk from definition docs and returns
// it as a slice (empty if no definitions). Extracted to keep ProcessFile's
// nesting depth within linter limits.
func (i *Indexer) buildTOCDocs(ctx context.Context, file string, defDocs []schema.Document) []schema.Document {
	if len(defDocs) == 0 {
		return nil
	}
	i.cfg.Logger.Debug("extracted definitions from file", "file", file, "definitions", len(defDocs))
	toc := buildTOCChunk(file, defDocs)
	if toc == nil {
		return nil
	}
	if sparseVec, err := sparse.GenerateSparseVector(ctx, toc.PageContent); err == nil {
		toc.Sparse = sparseVec
	}
	i.cfg.Logger.Debug("built TOC chunk", "file", file, "symbols", len(defDocs))
	return []schema.Document{*toc}
}

// buildLineOffsets creates a map of line number to byte offset.
func buildLineOffsets(content string) []int {
	var offsets []int
	offsets = append(offsets, 0) // Line 1 starts at offset 0

	for i, c := range content {
		if c == '\n' {
			offsets = append(offsets, i+1)
		}
	}

	return offsets
}

// findLineNumbers finds the start and end line numbers for a chunk.
func findLineNumbers(fullContent, chunkContent string, lineOffsets []int) (startLine, endLine int, ok bool) {
	// Find where chunk starts in full content
	startIdx := strings.Index(fullContent, chunkContent)
	if startIdx == -1 {
		return 0, 0, false
	}
	endIdx := startIdx + len(chunkContent)

	// Binary search for start line
	startLine = 1
	for i, offset := range lineOffsets {
		if offset > startIdx {
			startLine = i
			break
		}
	}

	// Binary search for end line
	endLine = startLine
	for i, offset := range lineOffsets {
		if offset >= endIdx {
			endLine = i
			break
		}
	}

	// Make sure endLine is at least startLine
	if endLine < startLine {
		endLine = startLine
	}

	return startLine, endLine, true
}

// extractSymbolsFromChunk extracts identifier names from a code chunk.
func extractSymbolsFromChunk(chunk, ext string) []string {
	var symbols []string
	seen := make(map[string]bool)

	// Pattern for identifiers (exported and unexported)
	// Go-style: PascalCase or camelCase
	// TypeScript/JavaScript: PascalCase, camelCase, snake_case
	// Python: snake_case, PascalCase for classes

	var patterns []*regexp.Regexp

	switch ext {
	case ".go":
		// Type references: Type{ or &Type{
		patterns = append(patterns, regexp.MustCompile(`&?([A-Z][a-zA-Z0-9]*)\s*\{`))
		// Method calls: .Method( or Method(
		patterns = append(patterns, regexp.MustCompile(`\.([A-Z][a-zA-Z0-9]*)\s*\(`))
		patterns = append(patterns, regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]*)\s*\(`))
		// Variable references
		patterns = append(patterns, regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9]*)\s*[,)\s]`))

	case ".ts", ".tsx", ".js", ".jsx":
		// Class/interface references
		patterns = append(patterns, regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]*)\s*[<{.]`))
		// Function calls
		patterns = append(patterns, regexp.MustCompile(`\b([a-zA-Z_$][a-zA-Z0-9_$]*)\s*\(`))

	case ".py":
		// Class references
		patterns = append(patterns, regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]*)\s*[(:,]`))
		// Function calls
		patterns = append(patterns, regexp.MustCompile(`\b([a-z_][a-zA-Z0-9_]*)\s*\(`))

	default:
		// Generic: identifiers
		patterns = append(patterns, regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\b`))
	}

	for _, pattern := range patterns {
		matches := pattern.FindAllStringSubmatch(chunk, -1)
		for _, match := range matches {
			if len(match) > 1 && match[1] != "" {
				sym := match[1]
				// Skip keywords
				if isCodeKeyword(sym) {
					continue
				}
				if !seen[sym] {
					seen[sym] = true
					symbols = append(symbols, sym)
				}
			}
		}
	}

	// Limit to top 10 symbols to avoid bloat
	if len(symbols) > 10 {
		symbols = symbols[:10]
	}

	return symbols
}

// isCodeKeyword checks if a symbol is a language keyword.
func isCodeKeyword(sym string) bool {
	keywords := map[string]bool{
		// Go
		"package": true, "import": true, "func": true, "var": true, "const": true,
		"type": true, "struct": true, "interface": true, "map": true, "chan": true,
		"return": true, "if": true, "else": true, "for": true, "range": true,
		"switch": true, "case": true, "default": true, "select": true, "go": true,
		"defer": true, "break": true, "continue": true, "goto": true,
		"true": true, "false": true, "nil": true, "iota": true,
		"string": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true,
		"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
		"float32": true, "float64": true, "bool": true, "byte": true, "rune": true,
		"error": true, "any": true, "context": true,
		// Common
		"self": true, "this": true, "super": true, "class": true, "def": true,
		"public": true, "private": true, "protected": true, "static": true,
		"void": true, "null": true, "undefined": true, "new": true,
		"len": true, "cap": true, "make": true, "append": true, "copy": true,
		"delete": true, "close": true, "panic": true, "recover": true,
		"fmt": true, "strings": true, "errors": true, "json": true, "http": true,
	}
	return keywords[sym]
}
