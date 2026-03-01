package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

const rootDir = "root"

// ArchSummaryData holds data for the arch_summary prompt template.
type ArchSummaryData struct {
	Path    string
	Files   string
	Symbols string
	Imports string
}

// DirectoryInfo groups metadata for files within a directory.
type DirectoryInfo struct {
	Path        string
	Files       []string
	Symbols     []string
	Imports     []string
	ContentHash string
}

// GenerateArchSummaries generates architectural summaries for directories.
// If targetPaths is empty, all directories are processed.
func (r *ragService) GenerateArchSummaries(ctx context.Context, collectionName, embedderModelName, repoPath string, targetPaths []string) error {
	r.logger.Info("generating architectural summaries",
		"collection", collectionName,
		"repoPath", repoPath,
		"target_paths_count", len(targetPaths),
	)

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
	summaryCache := r.fetchSummaryCache(ctx, scopedStore)

	// Walk filesystem to discover directories and check cache
	dirsToProcess, cachedCount, err := r.discoverDirectories(repoPath, targetPaths, summaryCache)
	if err != nil {
		return fmt.Errorf("failed to walk directories: %w", err)
	}

	r.logger.Info("architectural summary cache check complete",
		"cached", cachedCount,
		"queued", len(dirsToProcess),
	)

	if len(dirsToProcess) == 0 {
		return nil
	}

	// Hydrate directory metadata and generate summaries

	// Generate summaries with a worker pool
	// Use 5 workers by default for better throughput with LLM API rate limits
	const defaultArchSummaryWorkers = 5
	archDocs := r.generateSummariesWithWorkerPool(ctx, dirsToProcess, defaultArchSummaryWorkers)

	if len(archDocs) == 0 {
		r.logger.Warn("no architectural summaries generated")
		return nil
	}

	// Store the architectural summaries
	_, err = scopedStore.AddDocuments(ctx, archDocs)
	if err != nil {
		return fmt.Errorf("failed to store architectural summaries: %w", err)
	}

	r.logger.Info("architectural summaries generated and stored",
		"summaries", len(archDocs),
	)

	return nil
}

// fetchSummaryCache loads existing arch summaries from the vector store for cache comparison.
func (r *ragService) fetchSummaryCache(ctx context.Context, scopedStore storage.ScopedVectorStore) map[string]string {
	cacheDocs, err := scopedStore.SimilaritySearch(ctx, "summary", 500,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "arch",
		}),
	)
	if err != nil {
		r.logger.Warn("failed to fetch existing summaries for cache", "error", err)
		return make(map[string]string)
	}

	summaryCache := make(map[string]string)
	for _, doc := range cacheDocs {
		source, _ := doc.Metadata["source"].(string)
		hash, _ := doc.Metadata["content_hash"].(string)
		if source != "" {
			summaryCache[source] = hash
		}
	}
	r.logger.Debug("built summary cache from qdrant", "count", len(summaryCache))
	return summaryCache
}

// discoverDirectories walks the repo and returns directories needing summary updates.
//
//nolint:gocognit
func (r *ragService) discoverDirectories(repoPath string, targetPaths []string, summaryCache map[string]string) (map[string]*DirectoryInfo, int, error) {
	dirsToProcess := make(map[string]*DirectoryInfo)
	cachedCount := 0

	// Recursive walk for initial indexing
	if len(targetPaths) == 0 {
		err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !d.IsDir() {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}

			relPath, _ := filepath.Rel(repoPath, path)
			if relPath == "." {
				relPath = rootDir
			}
			relPath = strings.ReplaceAll(relPath, "\\", "/")

			return r.processSingleDir(repoPath, path, relPath, summaryCache, dirsToProcess, &cachedCount)
		})
		return dirsToProcess, cachedCount, err
	}

	// Targeted walk for incremental sync
	uniqueDirs := make(map[string]struct{})

	for _, p := range targetPaths {
		_, err := r.validateAndJoinPath(repoPath, p)
		if err != nil {
			r.logger.Warn("invalid target path", "path", p, "error", err)
			continue
		}

		cleanP := filepath.Clean(p)
		dir := filepath.Dir(cleanP)
		// Traverse up to root
		for {
			uniqueDirs[dir] = struct{}{}
			if dir == "." || dir == "/" || dir == "" {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	// Always include root summary in targeted scans as it might change
	uniqueDirs["."] = struct{}{}

	for relDir := range uniqueDirs {
		// Securely join using validateAndJoinPath to prevent traversal and handle symlinks correctly
		fullPath, err := r.validateAndJoinPath(repoPath, relDir)
		if err != nil {
			r.logger.Warn("directory traversal detected or invalid path", "path", relDir, "error", err)
			continue
		}

		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue // Directory might have been deleted
		}

		displayRelPath := relDir
		if displayRelPath == "." {
			displayRelPath = rootDir
		}
		displayRelPath = strings.ReplaceAll(displayRelPath, "\\", "/")

		if err := r.processSingleDir(repoPath, fullPath, displayRelPath, summaryCache, dirsToProcess, &cachedCount); err != nil {
			r.logger.Warn("targeted scan failed for directory", "path", relDir, "error", err)
		}
	}

	return dirsToProcess, cachedCount, nil
}

func (r *ragService) processSingleDir(repoPath, fullPath, relPath string, summaryCache map[string]string, dirsToProcess map[string]*DirectoryInfo, cachedCount *int) error {
	info, hash, scanErr := r.scanDirectoryOnDisk(repoPath, fullPath, relPath)
	if scanErr != nil {
		return scanErr
	}
	if info == nil {
		return nil
	}

	if cachedHash, ok := summaryCache[relPath]; ok && cachedHash == hash {
		(*cachedCount)++
		return nil
	}

	info.ContentHash = hash
	dirsToProcess[relPath] = info
	return nil
}

func (r *ragService) getDirectoryPath(doc schema.Document) string {
	source, _ := doc.Metadata["source"].(string)
	if source == "" {
		return ""
	}

	dirPath := path.Dir(strings.ReplaceAll(source, "\\", "/"))
	if dirPath == "." {
		return rootDir
	}
	return dirPath
}

func (r *ragService) extractDocMetadata(doc schema.Document, info *DirectoryInfo) {
	source, _ := doc.Metadata["source"].(string)
	fileName := path.Base(strings.ReplaceAll(source, "\\", "/"))
	if !containsString(info.Files, fileName) {
		info.Files = append(info.Files, fileName)
	}

	identifier, _ := doc.Metadata["identifier"].(string)
	if identifier != "" {
		if !containsString(info.Symbols, identifier) {
			info.Symbols = append(info.Symbols, identifier)
		}
	}

	chunkType, _ := doc.Metadata["chunk_type"].(string)
	if chunkType != "" && identifier != "" {
		symbolDesc := fmt.Sprintf("%s: %s", chunkType, identifier)
		if !containsString(info.Symbols, symbolDesc) {
			info.Symbols = append(info.Symbols, symbolDesc)
		}
	}
}

// generateSummariesWithWorkerPool generates summaries using a bounded worker pool.
func (r *ragService) generateSummariesWithWorkerPool(ctx context.Context, dirInfos map[string]*DirectoryInfo, workers int) []schema.Document {
	type result struct {
		doc schema.Document
		err error
	}

	// Create channels
	jobs := make(chan *DirectoryInfo, len(dirInfos))
	results := make(chan result, len(dirInfos))

	// Start workers
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for info := range jobs {
				doc, err := r.generateSummaryForDirectory(ctx, info)
				results <- result{doc: doc, err: err}
			}
		}()
	}

	// Send jobs
	for _, info := range dirInfos {
		jobs <- info
	}
	close(jobs)

	// Wait and close results
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var archDocs []schema.Document
	for res := range results {
		if res.err != nil {
			r.logger.Warn("failed to generate summary", "error", res.err)
			continue
		}
		if res.doc.PageContent != "" {
			archDocs = append(archDocs, res.doc)
		}
	}

	return archDocs
}

// generateSummaryForDirectory generates an LLM-based architectural summary for one directory.
func (r *ragService) generateSummaryForDirectory(ctx context.Context, info *DirectoryInfo) (schema.Document, error) {
	// Prepare prompt data
	promptData := ArchSummaryData{
		Path:    info.Path,
		Files:   strings.Join(info.Files, "\n"),
		Symbols: strings.Join(info.Symbols, "\n"),
		Imports: strings.Join(info.Imports, "\n"),
	}

	prompt, err := r.promptMgr.Render(llm.ArchSummaryPrompt, promptData)
	if err != nil {
		return schema.Document{}, fmt.Errorf("failed to render arch summary prompt: %w", err)
	}

	// Generate with LLM
	response, err := llms.GenerateFromSinglePrompt(ctx, r.generatorLLM, prompt)
	if err != nil {
		return schema.Document{}, fmt.Errorf("failed to generate summary for %s: %w", info.Path, err)
	}

	// Create the architectural summary document
	doc := schema.NewDocument(response, map[string]any{
		"source":       info.Path,
		"chunk_type":   "arch",
		"content_hash": info.ContentHash,
		"generated_at": time.Now().Format(time.RFC3339),
		"file_count":   len(info.Files),
	})

	r.logger.Debug("generated architectural summary",
		"path", info.Path,
		"summary_length", len(response),
	)

	return doc, nil
}

// calculateDirectoryHash returns a short content hash for cache invalidation.
func calculateDirectoryHash(info *DirectoryInfo) string {
	content := strings.Join(info.Files, "|") + "||" + strings.Join(info.Symbols, "|")
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:8]) // First 8 bytes for brevity
}

// containsString checks if a slice contains a string.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// GetArchContextForPaths retrieves architectural summaries for the directories
// containing the given file paths.
func (r *ragService) GetArchContextForPaths(ctx context.Context, scopedStore storage.ScopedVectorStore, paths []string) (string, error) {
	// Extract unique directories from paths
	dirs := make(map[string]struct{})
	for _, p := range paths {
		dir := path.Dir(strings.ReplaceAll(p, "\\", "/"))
		if dir == "." {
			dir = rootDir
		}
		dirs[dir] = struct{}{}
	}

	if len(dirs) == 0 {
		return "", nil
	}

	var archContext strings.Builder
	seenDirs := make(map[string]struct{})

	// Search for each directory's summary
	for dir := range dirs {
		// Skip if already processed
		if _, seen := seenDirs[dir]; seen {
			continue
		}

		// Search for this directory's summary using filter
		query := fmt.Sprintf("Summary of directory %s", dir)
		docs, err := scopedStore.SimilaritySearch(ctx, query, 3,
			vectorstores.WithFilters(map[string]any{
				"chunk_type": "arch",
			}),
		)
		if err != nil {
			r.logger.Debug("failed to search arch summaries", "dir", dir, "error", err)
			continue
		}

		// Find the best match for this directory
		for _, doc := range docs {
			source, _ := doc.Metadata["source"].(string)
			if source == dir {
				archContext.WriteString(fmt.Sprintf("## %s\n%s\n\n", source, doc.PageContent))
				seenDirs[dir] = struct{}{}
				break
			}
		}
	}

	return archContext.String(), nil
}

// scanDirectoryOnDisk lists code files in a directory and computes a hash for cache invalidation.
func (r *ragService) scanDirectoryOnDisk(_, fullPath, relPath string) (*DirectoryInfo, string, error) {
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, "", err
	}

	var files []string
	var hashBuilder strings.Builder

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !llm.IsCodeExtension(ext) {
			continue
		}

		files = append(files, entry.Name())

		// Hash by name+size only. Don't use mtime—git resets it on clone/checkout.
		info, err := entry.Info()
		if err == nil {
			hashBuilder.WriteString(fmt.Sprintf("%s:%d|", entry.Name(), info.Size()))
		}
	}

	if len(files) == 0 {
		return nil, "", nil
	}

	sort.Strings(files)

	hash := sha256.Sum256([]byte(hashBuilder.String()))
	hexHash := hex.EncodeToString(hash[:8])

	info := &DirectoryInfo{
		Path:        relPath,
		Files:       files,
		Symbols:     []string{},
		Imports:     []string{},
		ContentHash: hexHash,
	}

	return info, hexHash, nil
}

// GenerateComparisonSummaries generates architectural summaries for multiple
// directories using multiple LLM models in parallel.
//

func (r *ragService) GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error) {
	r.logger.Info("generating multi-directory comparison summaries", "models", models, "paths", relPaths)

	results := make(map[string]map[string]string)
	resultsMu := &sync.RWMutex{}
	for _, model := range models {
		results[model] = make(map[string]string)
	}

	llmInstances := make(map[string]llms.Model)
	for _, modelName := range models {
		if llm, err := r.getOrCreateLLM(ctx, modelName); err == nil {
			llmInstances[modelName] = llm
		} else {
			r.logger.Warn("failed to pre-fetch LLM", "model", modelName, "error", err)
		}
	}

	g, ctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, 10)
	defer close(sem)

	for _, relPath := range relPaths {
		g.Go(func() error {
			return r.processDirectorySummaries(ctx, models, llmInstances, repoPath, relPath, results, resultsMu, sem)
		})
	}

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("parallel summary generation failed: %w", err)
	}

	return results, nil
}

func (r *ragService) processDirectorySummaries(ctx context.Context, models []string, llmInstances map[string]llms.Model, repoPath, relPath string, results map[string]map[string]string, resultsMu *sync.RWMutex, sem chan struct{}) error {
	// Acquire semaphore
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	path, err := r.validateAndJoinPath(repoPath, relPath)
	if err != nil {
		return err
	}

	info, _, err := r.scanDirectoryOnDisk(repoPath, path, relPath)
	if err != nil {
		r.logger.Warn("failed to scan directory for comparison", "path", relPath, "error", err)
		return nil
	}
	if info == nil {
		info = &DirectoryInfo{Path: relPath}
	}

	for _, modelName := range models {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		summary := r.generateSingleSummary(ctx, info, llmInstances[modelName])
		resultsMu.Lock()
		results[modelName][relPath] = summary
		resultsMu.Unlock()
	}
	return nil
}

// validateAndJoinPath safely joins repoPath and relPath,
// guarding against directory traversal and symlink escapes.
func (r *ragService) validateAndJoinPath(repoPath, relPath string) (string, error) {
	cleanRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("invalid repo path: %w", err)
	}
	// Resolve symlinks for base path too (e.g. handles macOS /var -> /private/var)
	if resolvedRepo, err := filepath.EvalSymlinks(cleanRepo); err == nil {
		cleanRepo = resolvedRepo
	}

	if relPath == "." || relPath == "" || relPath == "/" {
		return cleanRepo, nil
	}

	// Basic sanitization (defense-in-depth)
	if strings.Contains(relPath, "\x00") {
		return "", fmt.Errorf("path contains null byte")
	}

	path := filepath.Join(cleanRepo, relPath)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid join path: %w", err)
	}

	// CRITICAL: Ensure absPath is contained in cleanRepo *before* symlink resolution.
	// This prevents attacks where a non-existent path with ".." components is "cleaned"
	// by filepath.Clean (in the fallback below), effectively cancelling the ".." and
	// bypassing the check if we only checked specific paths.
	rel, err := filepath.Rel(cleanRepo, absPath)
	if err != nil {
		return "", fmt.Errorf("failed to get relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path traversal attempt detected: %s", relPath)
	}

	// Resolve symlinks to detect if the path *logically* points outside the repo.
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Path doesn't exist, but we already confirmed the unresolved path is contained.
			// Since it doesn't exist, it can't differ from the unresolved path (no symlinks to follow).
			// So it's safe to return the unresolved absolute path.
			return absPath, nil
		}
		return "", fmt.Errorf("symlink resolution failed: %w", err)
	}

	// Re-check containment after resolution (catches symlink pointing out)
	rel2, err := filepath.Rel(cleanRepo, resolvedPath)
	if err != nil {
		return "", fmt.Errorf("failed to get relative path after resolution: %w", err)
	}
	if strings.HasPrefix(rel2, "..") || filepath.IsAbs(rel2) {
		return "", fmt.Errorf("path traversal via symlink detected: %s", relPath)
	}

	return resolvedPath, nil
}

func (r *ragService) generateSingleSummary(ctx context.Context, info *DirectoryInfo, generator llms.Model) string {
	if generator == nil {
		return "Error: LLM not initialized"
	}

	promptData := ArchSummaryData{
		Path:    info.Path,
		Files:   strings.Join(info.Files, "\n"),
		Symbols: "N/A (Comparison Mode)",
		Imports: "N/A (Comparison Mode)",
	}

	prompt, err := r.promptMgr.Render(llm.ArchSummaryPrompt, promptData)
	if err != nil {
		return fmt.Sprintf("Error rendering prompt: %v", err)
	}

	summary, err := llms.GenerateFromSinglePrompt(ctx, generator, prompt)
	if err != nil {
		return fmt.Sprintf("Generation Error: %v", err)
	}
	return summary
}
