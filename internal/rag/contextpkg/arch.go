package contextpkg

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

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
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
func (b *builderImpl) GenerateArchSummaries(ctx context.Context, collectionName, embedderModelName, repoPath string, targetPaths []string) error {
	b.cfg.Logger.Info("generating architectural summaries",
		"collection", collectionName,
		"repoPath", repoPath,
		"target_paths_count", len(targetPaths),
	)

	scopedStore := b.cfg.VectorStore.ForRepo(collectionName, embedderModelName)
	summaryCache := b.fetchSummaryCache(ctx, scopedStore)

	// Walk filesystem to discover directories and check cache
	dirsToProcess, cachedCount, err := b.discoverDirectories(repoPath, targetPaths, summaryCache)
	if err != nil {
		return fmt.Errorf("failed to walk directories: %w", err)
	}

	b.cfg.Logger.Info("architectural summary cache check complete",
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
	archDocs := b.generateSummariesWithWorkerPool(ctx, dirsToProcess, defaultArchSummaryWorkers)

	if len(archDocs) == 0 {
		b.cfg.Logger.Warn("no architectural summaries generated")
		return nil
	}

	// Store the architectural summaries
	_, err = scopedStore.AddDocuments(ctx, archDocs)
	if err != nil {
		return fmt.Errorf("failed to store architectural summaries: %w", err)
	}

	b.cfg.Logger.Info("architectural summaries generated and stored",
		"summaries", len(archDocs),
	)

	return nil
}

// fetchSummaryCache loads existing arch summaries from the vector store for cache comparison.
func (b *builderImpl) fetchSummaryCache(ctx context.Context, scopedStore storage.ScopedVectorStore) map[string]string {
	searchOpts := []vectorstores.Option{
		vectorstores.WithFilters(map[string]any{"chunk_type": "arch"}),
	}
	if b.cfg.AIConfig.RetrievalScoreThreshold > 0 {
		searchOpts = append(searchOpts, vectorstores.WithScoreThreshold(b.cfg.AIConfig.RetrievalScoreThreshold))
	}
	cacheDocs, err := scopedStore.SimilaritySearch(ctx, "summary", 500, searchOpts...)
	if err != nil {
		b.cfg.Logger.Warn("failed to fetch existing summaries for cache", "error", err)
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
	b.cfg.Logger.Debug("built summary cache from qdrant", "count", len(summaryCache))
	return summaryCache
}

// discoverDirectories walks the repo and returns directories needing summary updates.
//
//nolint:gocognit
func (b *builderImpl) discoverDirectories(repoPath string, targetPaths []string, summaryCache map[string]string) (map[string]*DirectoryInfo, int, error) {
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

			return b.processSingleDir(repoPath, path, relPath, summaryCache, dirsToProcess, &cachedCount)
		})
		return dirsToProcess, cachedCount, err
	}

	// Targeted walk for incremental sync
	uniqueDirs := make(map[string]struct{})

	for _, p := range targetPaths {
		_, err := b.validateAndJoinPath(repoPath, p)
		if err != nil {
			b.cfg.Logger.Warn("invalid target path", "path", p, "error", err)
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
		fullPath, err := b.validateAndJoinPath(repoPath, relDir)
		if err != nil {
			b.cfg.Logger.Warn("directory traversal detected or invalid path", "path", relDir, "error", err)
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

		if err := b.processSingleDir(repoPath, fullPath, displayRelPath, summaryCache, dirsToProcess, &cachedCount); err != nil {
			b.cfg.Logger.Warn("targeted scan failed for directory", "path", relDir, "error", err)
		}
	}

	return dirsToProcess, cachedCount, nil
}

func (b *builderImpl) processSingleDir(repoPath, fullPath, relPath string, summaryCache map[string]string, dirsToProcess map[string]*DirectoryInfo, cachedCount *int) error {
	info, hash, scanErr := b.scanDirectoryOnDisk(repoPath, fullPath, relPath)
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

// generateSummariesWithWorkerPool generates summaries using a bounded worker pool.
func (b *builderImpl) generateSummariesWithWorkerPool(ctx context.Context, dirInfos map[string]*DirectoryInfo, workers int) []schema.Document {
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
				doc, err := b.generateSummaryForDirectory(ctx, info)
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
			b.cfg.Logger.Warn("failed to generate summary", "error", res.err)
			continue
		}
		if res.doc.PageContent != "" {
			archDocs = append(archDocs, res.doc)
		}
	}

	return archDocs
}

// generateSummaryForDirectory generates an LLM-based architectural summary for one directory.
func (b *builderImpl) generateSummaryForDirectory(ctx context.Context, info *DirectoryInfo) (schema.Document, error) {
	// Prepare prompt data
	promptData := ArchSummaryData{
		Path:    info.Path,
		Files:   strings.Join(info.Files, "\n"),
		Symbols: strings.Join(info.Symbols, "\n"),
		Imports: strings.Join(info.Imports, "\n"),
	}

	prompt, err := b.cfg.PromptMgr.Render(llm.ArchSummaryPrompt, promptData)
	if err != nil {
		return schema.Document{}, fmt.Errorf("failed to render arch summary prompt: %w", err)
	}

	// Generate with LLM
	response, err := llms.GenerateFromSinglePrompt(ctx, b.cfg.GeneratorLLM, prompt)
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

	// Generate sparse vector for hybrid search
	sparseVec, err := sparse.GenerateSparseVector(ctx, response)
	if err == nil {
		doc.Sparse = sparseVec
	} else {
		b.cfg.Logger.Debug("failed to generate sparse vector for arch summary", "path", info.Path, "error", err)
	}

	b.cfg.Logger.Info("generated architectural summary",
		"path", info.Path,
		"summary_length", len(response),
	)

	return doc, nil
}

// GetArchContextForPaths retrieves architectural summaries for the directories
// containing the given file paths.
func (b *builderImpl) GetArchContextForPaths(ctx context.Context, scopedStore storage.ScopedVectorStore, paths []string) (string, error) {
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

		// Fetch this directory's summary by exact source match.
		// Filtering by both chunk_type and source means we always get the right
		// document without relying on top-K similarity ranking.
		archSearchOpts := []vectorstores.Option{
			vectorstores.WithFilters(map[string]any{
				"chunk_type": "arch",
				"source":     dir,
			}),
		}
		docs, err := scopedStore.SimilaritySearch(ctx, dir, 1, archSearchOpts...)
		if err != nil {
			b.cfg.Logger.Debug("failed to search arch summaries", "dir", dir, "error", err)
			continue
		}

		if len(docs) > 0 {
			fmt.Fprintf(&archContext, "## %s\n%s\n\n", dir, docs[0].PageContent)
			seenDirs[dir] = struct{}{}
		} else {
			b.cfg.Logger.Debug("no arch summary found for directory", "dir", dir)
		}
	}

	b.cfg.Logger.Debug("arch context assembled", "dirs_found", len(seenDirs), "dirs_queried", len(dirs))
	return archContext.String(), nil
}

//nolint:unparam // error always nil but signature required for errgroup
func (b *builderImpl) gatherArchContextSafe(ctx context.Context, store storage.ScopedVectorStore, files []internalgithub.ChangedFile) (string, error) {
	b.cfg.Logger.Info("stage started", "name", "ArchitecturalContext")
	ac := b.getArchContext(ctx, store, files)
	b.cfg.Logger.Info("stage completed", "name", "ArchitecturalContext")
	return ac, nil
}

func (b *builderImpl) getArchContext(ctx context.Context, scopedStore storage.ScopedVectorStore, files []internalgithub.ChangedFile) string {
	filePaths := make([]string, len(files))
	for i, f := range files {
		filePaths[i] = f.Filename
	}
	archContext, err := b.GetArchContextForPaths(ctx, scopedStore, filePaths)
	if err != nil {
		b.cfg.Logger.Warn("failed to get architectural context", "error", err)
		return ""
	}
	if archContext != "" {
		b.cfg.Logger.Debug("retrieved architectural context", "folders_count", len(filePaths))
	}
	return archContext
}

// scanDirectoryOnDisk lists code files in a directory, extracts symbols and imports,
// and computes a hash for cache invalidation.
func (b *builderImpl) scanDirectoryOnDisk(_ string, fullPath, relPath string) (*DirectoryInfo, string, error) {
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, "", err
	}

	var files []string
	var allImports, allSymbols []string
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
			fmt.Fprintf(&hashBuilder, "%s:%d|", entry.Name(), info.Size())
		}

		// Extract symbols and imports using parser registry
		if b.cfg.ParserRegistry != nil {
			filePath := filepath.Join(fullPath, entry.Name())
			imports, symbols := b.extractFileMetadata(filePath, entry.Name())
			allImports = append(allImports, imports...)
			allSymbols = append(allSymbols, symbols...)
		}
	}

	if len(files) == 0 {
		return nil, "", nil
	}

	sort.Strings(files)

	hash := sha256.Sum256([]byte(hashBuilder.String()))
	hexHash := hex.EncodeToString(hash[:8])

	// Deduplicate and sort imports and symbols
	allImports = dedupeAndSort(allImports, 50)  // Limit to top 50 unique imports
	allSymbols = dedupeAndSort(allSymbols, 100) // Limit to top 100 unique symbols

	dirInfo := &DirectoryInfo{
		Path:        relPath,
		Files:       files,
		Symbols:     allSymbols,
		Imports:     allImports,
		ContentHash: hexHash,
	}

	return dirInfo, hexHash, nil
}

// extractFileMetadata reads a file and extracts imports and exported symbols.
func (b *builderImpl) extractFileMetadata(filePath, fileName string) (imports, symbols []string) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		b.cfg.Logger.Debug("failed to read file for metadata extraction", "file", fileName, "error", err)
		return nil, nil
	}

	parser, err := b.cfg.ParserRegistry.GetParserForExtension(filepath.Ext(fileName))
	if err != nil {
		b.cfg.Logger.Debug("no parser for extension", "file", fileName, "ext", filepath.Ext(fileName))
		return nil, nil
	}

	metadata, err := parser.ExtractMetadata(string(content), filePath)
	if err != nil {
		b.cfg.Logger.Debug("failed to extract metadata", "file", fileName, "error", err)
		return nil, nil
	}

	// Extract imports
	imports = append(imports, metadata.Imports...)

	// Extract exported symbols (definitions with public visibility)
	for _, def := range metadata.Definitions {
		if def.Visibility == "public" {
			symbols = append(symbols, fmt.Sprintf("%s %s", def.Name, def.Type))
		}
	}

	return imports, symbols
}

// dedupeAndSort removes duplicates and sorts a slice, limiting to maxLen items.
func dedupeAndSort(items []string, maxLen int) []string {
	if len(items) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var result []string
	for _, item := range items {
		if item == "" {
			continue
		}
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}

	sort.Strings(result)
	if len(result) > maxLen {
		result = result[:maxLen]
	}
	return result
}

// GenerateComparisonSummaries generates architectural summaries for multiple
// directories using multiple LLM models in parallel.
//

func (b *builderImpl) GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error) {
	b.cfg.Logger.Info("generating multi-directory comparison summaries", "models", models, "paths", relPaths)

	results := make(map[string]map[string]string)
	resultsMu := &sync.RWMutex{}
	for _, model := range models {
		results[model] = make(map[string]string)
	}

	llmInstances := make(map[string]llms.Model)
	for _, modelName := range models {
		if llm, err := b.cfg.GetLLM(ctx, modelName); err == nil {
			llmInstances[modelName] = llm
		} else {
			b.cfg.Logger.Warn("failed to pre-fetch LLM", "model", modelName, "error", err)
		}
	}

	g, ctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, 10)
	for _, relPath := range relPaths {
		g.Go(func() error {
			return b.processDirectorySummaries(ctx, models, llmInstances, repoPath, relPath, results, resultsMu, sem)
		})
	}

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("parallel summary generation failed: %w", err)
	}

	return results, nil
}

func (b *builderImpl) processDirectorySummaries(ctx context.Context, models []string, llmInstances map[string]llms.Model, repoPath, relPath string, results map[string]map[string]string, resultsMu *sync.RWMutex, sem chan struct{}) error {
	// Acquire semaphore
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	path, err := b.validateAndJoinPath(repoPath, relPath)
	if err != nil {
		return err
	}

	info, _, err := b.scanDirectoryOnDisk(repoPath, path, relPath)
	if err != nil {
		b.cfg.Logger.Warn("failed to scan directory for comparison", "path", relPath, "error", err)
		return nil
	}
	if info == nil {
		info = &DirectoryInfo{Path: relPath}
	}

	for _, modelName := range models {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		summary := b.generateSingleSummary(ctx, info, llmInstances[modelName])
		resultsMu.Lock()
		results[modelName][relPath] = summary
		resultsMu.Unlock()
	}
	return nil
}

// validateAndJoinPath safely joins repoPath and relPath,
// guarding against directory traversal and symlink escapes.
func (b *builderImpl) validateAndJoinPath(repoPath, relPath string) (string, error) {
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

func (b *builderImpl) generateSingleSummary(ctx context.Context, info *DirectoryInfo, generator llms.Model) string {
	if generator == nil {
		return "Error: LLM not initialized"
	}

	promptData := ArchSummaryData{
		Path:    info.Path,
		Files:   strings.Join(info.Files, "\n"),
		Symbols: "N/A (Comparison Mode)",
		Imports: "N/A (Comparison Mode)",
	}

	prompt, err := b.cfg.PromptMgr.Render(llm.ArchSummaryPrompt, promptData)
	if err != nil {
		return fmt.Sprintf("Error rendering prompt: %v", err)
	}

	summary, err := llms.GenerateFromSinglePrompt(ctx, generator, prompt)
	if err != nil {
		return fmt.Sprintf("Generation Error: %v", err)
	}
	return summary
}
