package index

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
}

// Indexer handles document ingestion and semantic chunking.
type Indexer struct {
	cfg Config
}

// New creates a new [Indexer] instance.
func New(cfg Config) *Indexer {
	return &Indexer{cfg: cfg}
}

// SetupRepoContext indexes a repository for the first time or re-indexes
// using smart scan (file-hash based skipping).
//
//nolint:cyclop,gocyclo,gocognit,funlen // orchestrates complex smart-scan workflow
func (i *Indexer) SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string) error {
	i.cfg.Logger.Info("performing smart indexing with GoFrame GitLoader",
		"path", repoPath,
		"collection", repo.QdrantCollectionName,
	)
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	finalExcludeDirs := BuildExcludeDirs(repoConfig)
	startTime := time.Now()

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

	// Initialize GoFrame's GitLoader for streaming ingestion
	loader, err := documentloaders.NewGit(repoPath, i.cfg.ParserRegistry,
		documentloaders.WithExcludeDirs(finalExcludeDirs),
		documentloaders.WithExcludeExts(repoConfig.ExcludeExts),
		documentloaders.WithWorkerCount(4),
		documentloaders.WithGeneratedCodeDetection(true),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize git loader: %w", err)
	}

	scopedStore := i.cfg.VectorStore.ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName)
	var processedCount int
	var skippedCount int
	var mu sync.Mutex

	// Keep track of all files processed by the loader to identify deletions later
	filesProcessedByLoader := make(map[string]struct{})
	var filesProcessedByLoaderMu sync.Mutex

	// Worker pool for parallel file hashing and splitting
	const numHashWorkers = 4
	const batchSize = 500 // Limit memory usage

	type fileWork struct {
		file     string
		docs     []schema.Document
		filePath string
	}

	type fileResult struct {
		docsToInsert []schema.Document
		fileToUpdate storage.FileRecord
		processed    bool
		skipped      bool
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

					// Check if we can skip this file (using copied map)
					if hash != "" {
						if rec, exists := existingFilesCopy[work.file]; exists && rec.FileHash == hash {
							resultChan <- fileResult{processed: true, skipped: true}
							continue
						}
					}

					// Apply Code-Aware chunking
					split, err := i.cfg.Splitter.SplitDocuments(ctx, work.docs)
					if err != nil {
						i.cfg.Logger.Warn("splitting failed, using original chunks", "file", work.file, "error", err)
						split = work.docs
					}

					fileRec := storage.FileRecord{}
					if hash != "" {
						fileRec = storage.FileRecord{
							RepositoryID: repo.ID,
							FilePath:     work.file,
							FileHash:     hash,
						}
					}

					resultChan <- fileResult{docsToInsert: split, fileToUpdate: fileRec, processed: true}
				}
			}
		}()
	}

	// Start result collector goroutine to prevent deadlock
	var allProcessed int
	var allSkipped int
	var resultsMu sync.Mutex

	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for res := range resultChan {
			resultsMu.Lock()
			if res.skipped {
				allSkipped++
			}
			if res.processed {
				allProcessed++
			}

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
		}
	}()

	// Phase 1: Stream ingestion with OOM protection
	err = loader.LoadAndProcessStream(ctx, func(_ context.Context, docs []schema.Document) error {
		// Group documents by source to apply SHA-skip logic effectively
		docsByFile := make(map[string][]schema.Document)
		for _, doc := range docs {
			source, _ := doc.Metadata["source"].(string)
			if source != "" {
				docsByFile[source] = append(docsByFile[source], doc)
			}
		}

		// Send all files to worker pool
		for file, fileDocs := range docsByFile {
			filesProcessedByLoaderMu.Lock()
			filesProcessedByLoader[file] = struct{}{}
			filesProcessedByLoaderMu.Unlock()

			fullPath := filepath.Join(repoPath, file)
			select {
			case fileChan <- fileWork{
				file:     file,
				docs:     fileDocs,
				filePath: fullPath,
			}:
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

	// Update counts
	mu.Lock()
	processedCount = allProcessed
	skippedCount = allSkipped
	mu.Unlock()

	if err != nil {
		return fmt.Errorf("repository ingestion failed: %w", err)
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
			if err := i.cfg.VectorStore.DeleteDocumentsFromCollectionByFilter(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, map[string]any{"source": map[string]any{"$in": pathsToDelete}}); err != nil {
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
		if err := i.cfg.VectorStore.DeleteDocumentsFromCollection(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, filesToDelete); err != nil {
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
		scopedStore := i.cfg.VectorStore.ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName)
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

	// Get file extension for language detection
	ext := strings.ToLower(filepath.Ext(file))

	for idx := range splitDocs {
		// Ensure sparse vectors are generated for hybrid search if possible
		sparseVec, err := sparse.GenerateSparseVector(ctx, splitDocs[idx].PageContent)
		if err == nil {
			splitDocs[idx].Sparse = sparseVec
		}

		// Set chunk_type explicitly for code chunks
		splitDocs[idx].Metadata["chunk_type"] = "code"
		splitDocs[idx].Metadata["language"] = ext

		// Polyfill: Ensure is_test is set based on filename
		if IsTestFile(file) {
			splitDocs[idx].Metadata["is_test"] = true
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

		if len(defDocs) > 0 {
			i.cfg.Logger.Debug("extracted definitions from file",
				"file", file,
				"definitions", len(defDocs))
		}
	}

	return allDocs
}
