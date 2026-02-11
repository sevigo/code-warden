package llm

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

// GenerateArchSummaries generates architectural summaries for directories in the repository.
// It uses filesystem walking to discover directories and checks for existing summaries in batch.
func (r *ragService) GenerateArchSummaries(ctx context.Context, collectionName, embedderModelName, repoPath string) error {
	r.logger.Info("generating architectural summaries",
		"collection", collectionName,
		"repoPath", repoPath,
	)

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
	summaryCache := r.fetchSummaryCache(ctx, scopedStore)

	// Walk filesystem to discover directories and check cache
	dirsToProcess, cachedCount, err := r.discoverDirectories(repoPath, summaryCache)
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

	// 3. For directories that need updates, we need to gather Symbols and Imports.
	// We defer this data gathering to the worker pool phase to avoid blocking the discovery loop.
	// The hydration of directory metadata happens efficiently just before the LLM prompt value generation.

	// Generate summaries with a worker pool
	archDocs := r.generateSummariesWithWorkerPool(ctx, dirsToProcess, 3)

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

// fetchSummaryCache fetches existing architectural summaries from Qdrant to build a cache map.
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

// discoverDirectories walks the repo filesystem and returns directories needing summary updates.
func (r *ragService) discoverDirectories(repoPath string, summaryCache map[string]string) (map[string]*DirectoryInfo, int, error) {
	dirsToProcess := make(map[string]*DirectoryInfo)
	cachedCount := 0

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

		info, hash, scanErr := r.scanDirectoryOnDisk(repoPath, path, relPath)
		if scanErr != nil {
			r.logger.Warn("failed to scan directory", "path", path, "error", scanErr)
			return nil
		}
		if info == nil {
			return nil
		}

		if cachedHash, ok := summaryCache[relPath]; ok && cachedHash == hash {
			cachedCount++
			return nil
		}

		info.ContentHash = hash
		dirsToProcess[relPath] = info
		return nil
	})

	return dirsToProcess, cachedCount, err
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

// generateSummariesWithWorkerPool generates summaries using a limited worker pool.
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
	for i := range workers {
		_ = i
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

// generateSummaryForDirectory generates an architectural summary for a single directory.
func (r *ragService) generateSummaryForDirectory(ctx context.Context, info *DirectoryInfo) (schema.Document, error) {
	// Prepare prompt data
	promptData := ArchSummaryData{
		Path:    info.Path,
		Files:   strings.Join(info.Files, "\n"),
		Symbols: strings.Join(info.Symbols, "\n"),
		Imports: strings.Join(info.Imports, "\n"),
	}

	// Render the prompt
	prompt, err := r.promptMgr.Render(ArchSummaryPrompt, DefaultProvider, promptData)
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

// calculateDirectoryHash creates a hash of directory contents for cache invalidation.
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

// GetArchContextForPaths retrieves architectural summaries for given file paths.
// It extracts unique directories and searches for arch summaries with filters.
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

// scanDirectoryOnDisk finds code files in a directory and computes a hash for cache invalidation.
// Uses mtime+size for speed; robust enough for typical development workflows.
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
		if !isCodeExtension(ext) {
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

func isCodeExtension(ext string) bool {
	switch ext {
	// Add common code extensions here
	case ".go", ".js", ".ts", ".py", ".java", ".c", ".cpp", ".h", ".rs", ".rb", ".php", ".cs", ".swift", ".kt", ".scala":
		return true
	default:
		return false
	}
}

// GenerateComparisonSummaries generates architectural summaries for multiple directories using multiple models.
// GenerateComparisonSummaries generates architectural summaries for multiple directories using multiple models.
// It uses parallel execution to speed up the process, with a semaphore to limit concurrency.
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
		if llm, err := r.getOrCreateLLM(modelName); err == nil {
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
		summary := r.generateSingleSummary(ctx, modelName, relPath, info, llmInstances[modelName])
		resultsMu.Lock()
		results[modelName][relPath] = summary
		resultsMu.Unlock()
	}
	return nil
}

func (r *ragService) validateAndJoinPath(repoPath, relPath string) (string, error) {
	cleanRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("invalid repo path: %w", err)
	}

	if relPath == "." || relPath == "" || relPath == "/" {
		return cleanRepo, nil
	}

	path := filepath.Join(cleanRepo, relPath)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid join path: %w", err)
	}

	rel, err := filepath.Rel(cleanRepo, absPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path traversal attempt detected: %s", relPath)
	}
	return absPath, nil
}

func (r *ragService) generateSingleSummary(ctx context.Context, modelName, _ string, info *DirectoryInfo, llm llms.Model) string {
	if llm == nil {
		return "Error: LLM not initialized"
	}

	promptData := ArchSummaryData{
		Path:    info.Path,
		Files:   strings.Join(info.Files, "\n"),
		Symbols: "N/A (Comparison Mode)",
		Imports: "N/A (Comparison Mode)",
	}

	prompt, err := r.promptMgr.Render(ArchSummaryPrompt, ModelProvider(modelName), promptData)
	if err != nil {
		return fmt.Sprintf("Error rendering prompt: %v", err)
	}

	summary, err := llms.GenerateFromSinglePrompt(ctx, llm, prompt)
	if err != nil {
		return fmt.Sprintf("Generation Error: %v", err)
	}
	return summary
}
