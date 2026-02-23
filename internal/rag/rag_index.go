package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/goframe/documentloaders"
	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// SetupRepoContext processes a repository for the first time or re-indexes it using Smart Scan.
//
//nolint:gocognit,funlen // This function implements complex smart-scan logic that is difficult to split without losing context.
func (r *ragService) SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string) error {
	r.logger.Info("performing smart indexing with GoFrame GitLoader",
		"path", repoPath,
		"collection", repo.QdrantCollectionName,
	)
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	finalExcludeDirs := r.buildExcludeDirs(repoConfig)
	startTime := time.Now()

	// Smart Scan: Fetch existing file states for fast skipping
	existingFiles, err := r.store.GetFilesForRepo(ctx, repo.ID)
	if err != nil {
		r.logger.Warn("failed to fetch existing file states", "error", err)
		existingFiles = make(map[string]storage.FileRecord)
	}

	// Initialize GoFrame's GitLoader for streaming ingestion
	loader, err := documentloaders.NewGit(repoPath, r.parserRegistry,
		documentloaders.WithExcludeDirs(finalExcludeDirs),
		documentloaders.WithExcludeExts(repoConfig.ExcludeExts),
		documentloaders.WithWorkerCount(4),
		documentloaders.WithGeneratedCodeDetection(true),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize git loader: %w", err)
	}

	scopedStore := r.vectorStore.ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName)
	processedCount := 0
	skippedCount := 0
	var mu sync.Mutex

	// Keep track of all files processed by the loader to identify deletions later
	filesProcessedByLoader := make(map[string]struct{})
	var filesProcessedByLoaderMu sync.Mutex

	// Phase 1: Stream ingestion with OOM protection
	err = loader.LoadAndProcessStream(ctx, func(ctx context.Context, docs []schema.Document) error {
		// Group documents by source to apply SHA-skip logic effectively
		docsByFile := make(map[string][]schema.Document)
		for _, doc := range docs {
			source, _ := doc.Metadata["source"].(string)
			if source != "" {
				docsByFile[source] = append(docsByFile[source], doc)
			}
		}

		var docsToInsert []schema.Document
		var filesToUpdate []storage.FileRecord

		for file, fileDocs := range docsByFile {
			filesProcessedByLoaderMu.Lock()
			filesProcessedByLoader[file] = struct{}{}
			filesProcessedByLoaderMu.Unlock()

			fullPath := filepath.Join(repoPath, file)
			hash, err := computeFileHash(fullPath)
			if err != nil {
				r.logger.Warn("hash failed, will re-process", "file", file, "error", err)
			} else if rec, exists := existingFiles[file]; exists && rec.FileHash == hash {
				mu.Lock()
				skippedCount++
				mu.Unlock()
				continue
			}

			// Apply Code-Aware chunking to the retrieved documents (Part 2 instruction)
			split, err := r.splitter.SplitDocuments(ctx, fileDocs)
			if err != nil {
				r.logger.Warn("splitting failed, using original chunks", "file", file, "error", err)
				split = fileDocs
			}

			docsToInsert = append(docsToInsert, split...)
			if hash != "" {
				filesToUpdate = append(filesToUpdate, storage.FileRecord{
					RepositoryID: repo.ID,
					FilePath:     file,
					FileHash:     hash,
				})
			}
		}

		if len(docsToInsert) > 0 {
			if _, err := scopedStore.AddDocuments(ctx, docsToInsert); err != nil {
				return fmt.Errorf("failed to add vectors: %w", err)
			}

			// Update repository tracking in DB
			if err := r.store.UpsertFiles(ctx, repo.ID, filesToUpdate); err != nil {
				r.logger.Error("failed to update file state in DB", "error", err)
			}
		}

		mu.Lock()
		processedCount += len(docsByFile) // Count all processed files, even if 0 docs
		mu.Unlock()
		return nil
	})

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
		r.logger.Info("pruning deleted files from tracking", "count", len(pathsToDelete))
		if err := r.store.DeleteFiles(ctx, repo.ID, pathsToDelete); err != nil {
			r.logger.Warn("failed to delete stale file records", "error", err)
		}
		// Also remove from Qdrant?
		// We assume Qdrant clean up is handled via re-indexing or manual pruned?
		// Actually `processFilesParallel` handles UPSERT.
		// Deleting from Qdrant requires `DeleteDocumentsByFilter` ("source" in pathsToDelete).
		if len(pathsToDelete) > 0 && repo.QdrantCollectionName != "" {
			if err := r.vectorStore.DeleteDocumentsFromCollectionByFilter(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, map[string]any{"source": pathsToDelete}); err != nil {
				r.logger.Warn("failed to delete vectors for removed files", "error", err)
			}
		}
	}

	// Generate architectural summaries for directories (post-processing)
	if err := r.GenerateArchSummaries(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repoPath, nil); err != nil {
		r.logger.Warn("failed to generate architectural summaries, continuing without them", "error", err)
	}

	r.logger.Info("repository setup complete",
		"indexed_files", processedCount,
		"skipped_files", skippedCount,
		"duration", time.Since(startTime).Round(time.Second),
	)

	return nil
}

// computeFileHash calculates SHA256 hash of a file
func computeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// UpdateRepoContext incrementally updates the vector store based on file changes.
func (r *ragService) UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string) error {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	// Get the same exclude directories configuration as SetupRepoContext
	finalExcludeDirs := r.buildExcludeDirs(repoConfig)

	// Apply directory filtering first, then extension filtering, then specific file filtering
	filesToProcess = r.filterFilesByDirectories(filesToProcess, finalExcludeDirs)
	filesToDelete = r.filterFilesByDirectories(filesToDelete, finalExcludeDirs)

	// Apply valid extension whitelist (same as scanner)
	filesToProcess = filterFilesByValidExtensions(filesToProcess)
	filesToDelete = filterFilesByValidExtensions(filesToDelete)

	filesToProcess = filterFilesByExtensions(filesToProcess, repoConfig.ExcludeExts)
	filesToDelete = filterFilesByExtensions(filesToDelete, repoConfig.ExcludeExts)

	filesToProcess = filterFilesBySpecificFiles(filesToProcess, repoConfig.ExcludeFiles)
	filesToDelete = filterFilesBySpecificFiles(filesToDelete, repoConfig.ExcludeFiles)

	r.logger.Info("updating repository context after filtering",
		"collection", repo.QdrantCollectionName,
		"process", len(filesToProcess),
		"delete", len(filesToDelete),
		"exclude_dirs", finalExcludeDirs,
		"exclude_exts", repoConfig.ExcludeExts,
		"exclude_files", repoConfig.ExcludeFiles,
	)

	// Handle deleted files first
	if len(filesToDelete) > 0 {
		r.logger.Info("deleting embeddings for removed files", "count", len(filesToDelete))
		if err := r.vectorStore.DeleteDocumentsFromCollection(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, filesToDelete); err != nil {
			r.logger.Error("failed to delete some embeddings", "error", err)
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
				docs := r.ProcessFile(ctx, repoPath, f)
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

	var allDocs []schema.Document
	for res := range resultChan {
		allDocs = append(allDocs, res.docs...)
	}

	if len(allDocs) > 0 {
		r.logger.Info("adding/updating documents in vector store", "count", len(allDocs))
		scopedStore := r.vectorStore.ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName)
		if _, err := scopedStore.AddDocuments(ctx, allDocs); err != nil {
			return fmt.Errorf("failed to add/update embeddings for changed files: %w", err)
		}
	}

	// Trigger targeted arch summary re-generation
	if err := r.GenerateArchSummaries(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repoPath, append(filesToProcess, filesToDelete...)); err != nil {
		r.logger.Warn("failed to update architectural summaries after sync", "error", err)
	}

	return nil
}

// ProcessFile reads, parses, and chunks a single file.
func (r *ragService) ProcessFile(ctx context.Context, repoPath, file string) []schema.Document {
	fullPath := filepath.Join(repoPath, file)

	// Read file for chunking
	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		r.logger.Error("failed to read file for processing", "file", file, "error", err)
		return nil
	}

	// Sanitize content to ensure valid UTF-8.
	// Use GoFrame's code-aware splitter for OOM protection and exact graph navigation.
	// We wrap the raw content in a schema.Document and let the splitter handle it.
	validContent := strings.ToValidUTF8(string(contentBytes), "")
	doc := schema.NewDocument(validContent, map[string]any{
		"source": file,
	})

	splitDocs, err := r.splitter.SplitDocuments(ctx, []schema.Document{doc})
	if err != nil {
		r.logger.Error("failed to split document with code-aware splitter", "file", file, "error", err)
		return nil
	}

	for i := range splitDocs {
		// Ensure sparse vectors are generated for hybrid search if possible
		sparseVec, err := sparse.GenerateSparseVector(ctx, splitDocs[i].PageContent)
		if err == nil {
			splitDocs[i].Sparse = sparseVec
		}

		// Polyfill: Ensure is_test is set based on filename
		if isTestFile(file) {
			splitDocs[i].Metadata["is_test"] = true
		}
	}
	return splitDocs
}

func isTestFile(path string) bool {
	ext := filepath.Ext(path)
	base := filepath.Base(path)

	switch ext {
	case ".go":
		return strings.HasSuffix(base, "_test.go")
	case ".ts", ".js", ".tsx", ".jsx":
		return strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".test.js") ||
			strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".spec.js") ||
			strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.tsx")
	case ".py":
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
	case ".rs":
		return strings.HasSuffix(base, "_test.rs") // Rust conventions vary but often in-file or test_*.rs
	case ".java":
		return strings.HasSuffix(base, "Test.java") || strings.HasSuffix(base, "Tests.java")
	}
	return false
}

// isLogicFile returns true if the file is a likely code/logic file.
func isLogicFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return llm.IsCodeExtension(ext)
}

// filterFilesByExtensions removes files from a slice if their extension matches
// one of the provided excluded extensions.
func filterFilesByExtensions(files []string, excludeExts []string) []string {
	if len(excludeExts) == 0 {
		return files
	}

	excludeMap := make(map[string]struct{}, len(excludeExts))
	for _, ext := range excludeExts {
		normalizedExt := strings.ToLower(strings.TrimPrefix(ext, "."))
		excludeMap[normalizedExt] = struct{}{}
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		fileExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
		if _, isExcluded := excludeMap[fileExt]; !isExcluded {
			filtered = append(filtered, file)
		}
	}

	return filtered
}

// filterFilesByValidExtensions removes files from a slice if their extension is not
// in the whitelist of supported extensions. This ensures consistency with the scanner.
func filterFilesByValidExtensions(files []string) []string {
	validExts := map[string]bool{
		".go":   true,
		".js":   true,
		".ts":   true,
		".py":   true,
		".java": true,
		".c":    true,
		".cpp":  true,
		".h":    true,
		".rs":   true,
		".md":   true,
		".json": true,
		".yaml": true,
		".yml":  true,
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		ext := strings.ToLower(filepath.Ext(file))
		if validExts[ext] {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

// buildExcludeDirs creates the final list of directories to exclude, combining
// application defaults with user-configured exclusions.
func (r *ragService) buildExcludeDirs(repoConfig *core.RepoConfig) []string {
	appDefaultExcludeDirs := []string{".git", ".github", "vendor", "node_modules", "target", "build"}

	// Using a map handles duplicates automatically.
	allExcludeDirs := make(map[string]struct{})
	for _, dir := range appDefaultExcludeDirs {
		allExcludeDirs[dir] = struct{}{}
	}
	for _, dir := range repoConfig.ExcludeDirs {
		allExcludeDirs[dir] = struct{}{}
	}

	finalExcludeDirs := make([]string, 0, len(allExcludeDirs))
	for dir := range allExcludeDirs {
		finalExcludeDirs = append(finalExcludeDirs, dir)
	}

	return finalExcludeDirs
}

// filterFilesByDirectories removes files from a slice if they are located within
// any of the excluded directories.
func (r *ragService) filterFilesByDirectories(files []string, excludeDirs []string) []string {
	if len(excludeDirs) == 0 {
		return files
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		// Normalize the file path to forward slashes for cross-platform consistency
		cleanFile := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(file, string(filepath.Separator))))

		isExcluded := false
		for _, excludeDir := range excludeDirs {
			cleanExcludeDir := filepath.Clean(excludeDir)

			// Check if the file path is exactly the excluded directory
			if cleanFile == cleanExcludeDir {
				isExcluded = true
				break
			}

			// Check if the file path starts with the excluded directory followed by a separator
			// Use forward slash for cross-platform consistency
			if strings.HasPrefix(cleanFile, cleanExcludeDir+"/") {
				isExcluded = true
				break
			}
		}

		if !isExcluded {
			filtered = append(filtered, file)
		}
	}

	return filtered
}

// filterFilesBySpecificFiles removes files from a slice if their path matches
// one of the provided excluded specific files.
func filterFilesBySpecificFiles(files []string, excludeFiles []string) []string {
	if len(excludeFiles) == 0 {
		return files
	}

	excludeMap := make(map[string]struct{}, len(excludeFiles))
	for _, f := range excludeFiles {
		excludeMap[filepath.ToSlash(filepath.Clean(f))] = struct{}{}
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		if _, isExcluded := excludeMap[filepath.ToSlash(filepath.Clean(file))]; !isExcluded {
			filtered = append(filtered, file)
		}
	}

	return filtered
}
