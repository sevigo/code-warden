package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
)

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
// It groups indexed documents by directory, creates summaries via LLM, and stores them.
func (r *ragService) GenerateArchSummaries(ctx context.Context, collectionName, embedderModelName, repoPath string) error {
	r.logger.Info("generating architectural summaries",
		"collection", collectionName,
		"repoPath", repoPath,
	)

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)

	// Search for existing code documents to extract directory structure
	// We use a broad query to get many documents
	allDocs, err := scopedStore.SimilaritySearch(ctx, "code function type struct", 500)
	if err != nil {
		return fmt.Errorf("failed to fetch existing documents: %w", err)
	}

	if len(allDocs) == 0 {
		r.logger.Warn("no documents found to generate summaries from")
		return nil
	}

	// Group documents by directory
	dirInfos := r.groupDocumentsByDirectory(allDocs)
	r.logger.Info("found directories to summarize", "count", len(dirInfos))

	// Generate summaries with a worker pool
	archDocs := r.generateSummariesWithWorkerPool(ctx, dirInfos, 3) // 3 concurrent workers

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

// groupDocumentsByDirectory groups documents by their source directory.
func (r *ragService) groupDocumentsByDirectory(docs []schema.Document) map[string]*DirectoryInfo {
	dirInfos := make(map[string]*DirectoryInfo)

	for _, doc := range docs {
		dirPath := r.getDirectoryPath(doc)
		if dirPath == "" {
			continue
		}

		if _, exists := dirInfos[dirPath]; !exists {
			dirInfos[dirPath] = &DirectoryInfo{
				Path:    dirPath,
				Files:   []string{},
				Symbols: []string{},
				Imports: []string{},
			}
		}

		info := dirInfos[dirPath]
		r.extractDocMetadata(doc, info)
	}

	// Calculate content hash for each directory
	for _, info := range dirInfos {
		sort.Strings(info.Files)
		sort.Strings(info.Symbols)
		info.ContentHash = calculateDirectoryHash(info)
	}

	return dirInfos
}

func (r *ragService) getDirectoryPath(doc schema.Document) string {
	source, _ := doc.Metadata["source"].(string)
	if source == "" {
		return ""
	}

	dirPath := path.Dir(strings.ReplaceAll(source, "\\", "/"))
	if dirPath == "." {
		return "root"
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
			dir = "root"
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
