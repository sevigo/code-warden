// Package llm provides functionality for interacting with Large Language Models (LLMs),
// including prompt construction and Retrieval-Augmented Generation (RAG) workflows.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/textsplitter"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

type ComparisonResult struct {
	Model  string
	Review string
	Error  error
}

// RAGService defines the core operations for our Retrieval-Augmented Generation (RAG) pipeline.
type RAGService interface {
	SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string) error
	UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string) error
	GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client) (*core.StructuredReview, string, error)
	GenerateReReview(ctx context.Context, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client) (string, error)
	AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error)
	ProcessFile(repoPath, file string) []schema.Document
	GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error)
	GenerateComparisonReviews(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client, models []string, preFetchedDiff string, preFetchedFiles []internalgithub.ChangedFile) ([]ComparisonResult, error)
	GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client, models []string) (*core.StructuredReview, error)
}

type ragService struct {
	cfg            *config.Config
	promptMgr      *PromptManager
	vectorStore    storage.VectorStore
	store          storage.Store
	generatorLLM   llms.Model
	reranker       schema.Reranker
	parserRegistry parsers.ParserRegistry
	logger         *slog.Logger
	hydeCache      sync.Map // map[string]string: patchHash -> hydeSnippet
}

// NewRAGService creates a new RAGService instance with a vector store, LLM model,
// parser registry, and logger. This service powers the indexing and code review flow.
func NewRAGService(
	cfg *config.Config,
	promptMgr *PromptManager,
	vs storage.VectorStore,
	dbStore storage.Store,
	gen llms.Model,
	reranker schema.Reranker,
	pr parsers.ParserRegistry,
	logger *slog.Logger,
) RAGService {
	return &ragService{
		cfg:            cfg,
		promptMgr:      promptMgr,
		vectorStore:    vs,
		store:          dbStore,
		generatorLLM:   gen,
		reranker:       reranker,
		parserRegistry: pr,
		logger:         logger,
	}
}

// SetupRepoContext processes a repository for the first time or re-indexes it using Smart Scan.
// SetupRepoContext processes a repository for the first time or re-indexes it using Smart Scan.
//
//nolint:gocognit,funlen // This function implements complex smart-scan logic that is difficult to split without losing context.
func (r *ragService) SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string) error {
	r.logger.Info("performing smart indexing of repository",
		"path", repoPath,
		"collection", repo.QdrantCollectionName,
		"embedder", repo.EmbedderModelName,
	)
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	finalExcludeDirs := r.buildExcludeDirs(repoConfig)

	// Smart Scan: Fetch existing file states
	existingFiles, err := r.store.GetFilesForRepo(ctx, repo.ID)
	if err != nil {
		r.logger.Warn("failed to fetch existing file states (proceeding with full scan)", "error", err)
		existingFiles = make(map[string]storage.FileRecord)
	}

	startTime := time.Now()

	// Create a scoped store for this repository
	scopedStore := r.vectorStore.ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName)

	// IMPLEMENTATION OF PRE-SCAN
	filesOnDisk, err := listFiles(repoPath, finalExcludeDirs, repoConfig.ExcludeExts)
	if err != nil {
		return err
	}

	filesToProcess := make([]string, 0, len(filesOnDisk))
	skippedCount := 0

	// Collect file records to update later
	filesToUpdate := make([]storage.FileRecord, 0)

	for _, file := range filesOnDisk {
		fullPath := filepath.Join(repoPath, file)
		hash, err := computeFileHash(fullPath)
		if err != nil {
			r.logger.Warn("failed to hash file, will process", "file", file, "error", err)
			filesToProcess = append(filesToProcess, file)
			continue
		}

		if rec, exists := existingFiles[file]; exists && rec.FileHash == hash {
			// Unchanged!
			skippedCount++
			continue
		}

		// New or Changed
		filesToProcess = append(filesToProcess, file)
		filesToUpdate = append(filesToUpdate, storage.FileRecord{
			RepositoryID: repo.ID,
			FilePath:     file,
			FileHash:     hash,
		})
	}

	r.logger.Info("Smart Scan Analysis",
		"total_files", len(filesOnDisk),
		"unchanged_skipped", skippedCount,
		"to_index", len(filesToProcess),
	)

	if len(filesToProcess) == 0 {
		r.logger.Info("No files changed, skipping indexing.")
	}

	// Execute indexing and cleanup
	// We proceed regardless of count, but check filesToProcess for indexing

	// Now use processFilesParallel for the filtered list of files
	r.logger.Info("indexing changed files", "count", len(filesToProcess))
	allDocs := r.processFilesParallel(repoPath, filesToProcess, 8) // Parallelize

	// Index the documents
	if len(allDocs) > 0 {
		// Upsert documents in batches
		// Note: processFilesParallel returns all docs. For 17k files, this might be large.
		// But we are filtered now.

		_, err := scopedStore.AddDocuments(ctx, allDocs)
		if err != nil {
			return fmt.Errorf("failed to add documents to vector store: %w", err)
		}
	} else if len(filesToProcess) > 0 {
		r.logger.Warn("files marked for processing produced no documents (parser issue?)", "count", len(filesToProcess))
	}

	// Upsert file records to DB (mark them as indexed)
	if len(filesToUpdate) > 0 {
		r.logger.Info("updating file tracking in database", "count", len(filesToUpdate))
		if err := r.store.UpsertFiles(ctx, repo.ID, filesToUpdate); err != nil {
			r.logger.Error("failed to update file tracking records", "error", err)
			// Non-critical: we indexed them, but next time we might re-scan.
		}
	}

	// Cleanup: Delete records for files that no longer exist on disk
	// (We need to identify files in existingFiles but NOT in filesOnDisk)
	var pathsToDelete []string
	onDiskMap := make(map[string]struct{})
	for _, f := range filesOnDisk {
		onDiskMap[f] = struct{}{}
	}

	for path := range existingFiles {
		if _, onDisk := onDiskMap[path]; !onDisk {
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
	if err := r.GenerateArchSummaries(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repoPath); err != nil {
		r.logger.Warn("failed to generate architectural summaries, continuing without them", "error", err)
	}

	r.logger.Info("repository setup complete",
		"collection", repo.QdrantCollectionName,
		"newly_indexed_files", len(filesToProcess),
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

// listFiles recurses directory and returns list of relative paths
func listFiles(root string, excludeDirs, excludeExts []string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if isExcludedDir(info.Name(), excludeDirs) {
				return filepath.SkipDir
			}
			return nil
		}
		if isExcludedExt(info.Name(), excludeExts) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

func isExcludedDir(name string, excludes []string) bool {
	// Check hidden dirs
	if strings.HasPrefix(name, ".") && name != "." {
		return true
	}
	for _, ex := range excludes {
		if name == ex {
			return true
		}
	}
	return false
}

func isExcludedExt(name string, excludes []string) bool {
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	for _, ex := range excludes {
		if ext == ex {
			return true
		}
	}
	return false
}

// UpdateRepoContext incrementally updates the vector store based on file changes.
func (r *ragService) UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string) error {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	// Get the same exclude directories configuration as SetupRepoContext
	finalExcludeDirs := r.buildExcludeDirs(repoConfig)

	// Apply directory filtering first, then extension filtering
	filesToProcess = r.filterFilesByDirectories(filesToProcess, finalExcludeDirs)
	filesToDelete = r.filterFilesByDirectories(filesToDelete, finalExcludeDirs)

	filesToProcess = filterFilesByExtensions(filesToProcess, repoConfig.ExcludeExts)
	filesToDelete = filterFilesByExtensions(filesToDelete, repoConfig.ExcludeExts)

	r.logger.Info("updating repository context after filtering",
		"collection", repo.QdrantCollectionName,
		"process", len(filesToProcess),
		"delete", len(filesToDelete),
		"exclude_dirs", finalExcludeDirs,
		"exclude_exts", repoConfig.ExcludeExts,
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

	// Process files in parallel using worker pool
	allDocs := r.processFilesParallel(repoPath, filesToProcess, 4)

	if len(allDocs) > 0 {
		r.logger.Info("adding/updating documents in vector store", "count", len(allDocs))
		scopedStore := r.vectorStore.ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName)
		if _, err := scopedStore.AddDocuments(ctx, allDocs); err != nil {
			return fmt.Errorf("failed to add/update embeddings for changed files: %w", err)
		}
	}

	// Trigger arch summary re-generation for the repository
	if err := r.GenerateArchSummaries(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repoPath); err != nil {
		r.logger.Warn("failed to update architectural summaries after sync", "error", err)
	}

	return nil
}

// processFilesParallel processes files concurrently using a worker pool.
func (r *ragService) processFilesParallel(repoPath string, files []string, numWorkers int) []schema.Document {
	if len(files) == 0 {
		return nil
	}

	type result struct {
		docs []schema.Document
	}

	fileChan := make(chan string, len(files))
	resultChan := make(chan result, len(files))

	// Start workers
	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range fileChan {
				docs := r.ProcessFile(repoPath, file)
				resultChan <- result{docs: docs}
			}
		}()
	}

	// Send files to workers
	for _, file := range files {
		fileChan <- file
	}
	close(fileChan)

	// Wait for workers and close result channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	var allDocs []schema.Document
	for res := range resultChan {
		allDocs = append(allDocs, res.docs...)
	}
	return allDocs
}

// ProcessFile reads, parses, and chunks a single file.
func (r *ragService) ProcessFile(repoPath, file string) []schema.Document {
	fullPath := filepath.Join(repoPath, file)
	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		r.logger.Error("failed to read file for update, skipping", "file", file, "error", err)
		return nil
	}

	parser, err := r.parserRegistry.GetParserForFile(fullPath, nil)
	if err != nil {
		r.logger.Warn("no suitable parser found for file, skipping", "file", file, "error", err)
		return nil
	}

	// Sanitize content to ensure valid UTF-8.
	// This prevents "string field contains invalid UTF-8" errors in gRPC/Qdrant.
	validContent := strings.ToValidUTF8(string(contentBytes), "")

	chunks, err := parser.Chunk(validContent, file, nil)
	if err != nil {
		r.logger.Error("failed to chunk file", "file", file, "error", err)
		return nil
	}

	// Extract file-level metadata (imports, package name)
	var fileMeta map[string]any
	if meta, err := parser.ExtractMetadata(string(contentBytes), fullPath); err == nil {
		fileMeta = make(map[string]any)
		if meta.PackageName != "" {
			fileMeta["package_name"] = meta.PackageName
		}
		if len(meta.Imports) > 0 {
			fileMeta["imports"] = meta.Imports
		}
	}

	var docs []schema.Document
	for _, chunk := range chunks {
		// Generate deterministic ID to allow idempotent updates (deduplication)
		// ID = SHA256(FilePath + LineStart + LineEnd)
		h := sha256.New()
		h.Write([]byte(file))
		fmt.Fprintf(h, ":%d:%d", chunk.LineStart, chunk.LineEnd)
		sum := h.Sum(nil)
		// Format as UUID: 8-4-4-4-12
		id := fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])

		doc := schema.NewDocument(chunk.Content, map[string]any{
			"id":               id,
			"source":           file,
			"identifier":       chunk.Identifier,
			"chunk_type":       chunk.Type,
			"line_start":       chunk.LineStart,
			"line_end":         chunk.LineEnd,
			"parent_id":        chunk.ParentID,
			"full_parent_text": textsplitter.TruncateParentText(chunk.FullParentText, 2000), // Max 2000 chars
		})

		// Merge file-level metadata
		for k, v := range fileMeta {
			doc.Metadata[k] = v
		}

		// Copy annotations (e.g. is_test)
		for k, v := range chunk.Annotations {
			doc.Metadata[k] = v
		}

		// Explicit test file marking (polyfill)
		if isTestFile(file) {
			doc.Metadata["is_test"] = true
		}

		// Generate sparse vector for hybrid search
		sparseVec, err := sparse.GenerateSparseVector(context.Background(), chunk.Content)
		if err != nil {
			r.logger.Warn("failed to generate sparse vector for chunk", "file", file, "error", err)
		} else {
			doc.Sparse = sparseVec
		}

		docs = append(docs, doc)
	}
	return docs
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

// GenerateReview now focuses on data preparation and delegates to the helper.
func (r *ragService) GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client) (*core.StructuredReview, string, error) {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	r.logger.Info("preparing data for a full review", "repo", event.RepoFullName, "pr", event.PRNumber, "embedder", repo.EmbedderModelName)
	diff, err := ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get PR diff: %w", err)
	}
	if diff == "" {
		r.logger.Info("no code changes in pull request", "pr", event.PRNumber)
		noChangesReview := &core.StructuredReview{
			Summary:     "This pull request contains no code changes. Looks good to me!",
			Suggestions: []core.Suggestion{},
		}
		rawJSON, _ := json.Marshal(noChangesReview)
		return noChangesReview, string(rawJSON), nil
	}

	changedFiles, err := ghClient.GetChangedFiles(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get changed files: %w", err)
	}

	contextString := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, changedFiles)

	promptData := map[string]string{
		"Title":              event.PRTitle,
		"Description":        event.PRBody,
		"Language":           event.Language,
		"CustomInstructions": strings.Join(repoConfig.CustomInstructions, "\n"),
		"ChangedFiles":       r.formatChangedFiles(changedFiles),
		"Context":            contextString,
		"Diff":               diff,
	}

	rawReview, err := r.generateResponseWithPrompt(ctx, event, CodeReviewPrompt, promptData)
	if err != nil {
		return nil, "", err
	}

	// Parse the JSON string into the structured format
	var structuredReview core.StructuredReview

	// Use robust extraction helper
	jsonString, err := r.extractJSON(rawReview)
	if err != nil {
		r.logger.Error("failed to extract JSON from LLM response", "error", err, "raw_response", rawReview)
		return nil, "", fmt.Errorf("failed to extract JSON object: %w", err)
	}

	if err := json.Unmarshal([]byte(jsonString), &structuredReview); err != nil {
		r.logger.Error("failed to unmarshal LLM response", "error", err, "json", jsonString)
		return nil, "", fmt.Errorf("failed to parse LLM's JSON response: %w", err)
	}

	return &structuredReview, jsonString, nil
}

// GenerateComparisonReviews calculates common context once and performs final analysis with multiple models.
//
//nolint:gocognit,funlen // Complex logic with parallel execution and error aggregation
func (r *ragService) GenerateComparisonReviews(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client, models []string, preFetchedDiff string, preFetchedFiles []internalgithub.ChangedFile) ([]ComparisonResult, error) {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	diff := preFetchedDiff
	var changedFiles []internalgithub.ChangedFile
	if len(preFetchedFiles) > 0 {
		changedFiles = preFetchedFiles
	}

	// If data wasn't provided, fetch it (fallback for direct calls)
	if diff == "" {
		var err error
		diff, err = ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to get PR diff: %w", err)
		}
	}
	if len(changedFiles) == 0 {
		var err error
		changedFiles, err = ghClient.GetChangedFiles(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to get changed files: %w", err)
		}
	}

	contextString := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, changedFiles)

	// Reuse repoConfig logic
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	promptData := map[string]string{
		"Title":              event.PRTitle,
		"Description":        event.PRBody,
		"Language":           event.Language,
		"CustomInstructions": strings.Join(repoConfig.CustomInstructions, "\n"),
		"ChangedFiles":       r.formatChangedFiles(changedFiles),
		"Context":            contextString,
		"Diff":               diff,
	}

	results := make([]ComparisonResult, 0, len(models))
	var mu sync.Mutex

	g, ctx := errgroup.WithContext(ctx)
	// Limit concurrency to avoid rate limits
	// Review feedback: 3 is arbitrary. Bumping to 5 for now.
	const maxConcurrentModels = 5
	sem := make(chan struct{}, maxConcurrentModels)
	// No defer close(sem) - channel will be GC'd or we can close after Wait()

	for _, modelName := range models {
		modelName := modelName // Capture for closure safety
		g.Go(func() error {
			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				// Release only if acquired
				defer func() { <-sem }()
			case <-ctx.Done():
				return ctx.Err()
			}

			// Check context again before starting work
			if ctx.Err() != nil {
				return ctx.Err()
			}

			r.logger.Info("generating review summary", "model", modelName)

			// Con: Create local copy of promptData to ensure goroutine isolation (Priority 4)
			// Maps are passed by reference; defensive copy prevents race conditions
			// if caller modifies the original map during parallel execution.
			localPromptData := make(map[string]string, len(promptData))
			for k, v := range promptData {
				localPromptData[k] = v
			}

			llmModel, err := r.getOrCreateLLM(modelName)
			if err != nil {
				mu.Lock()
				results = append(results, ComparisonResult{Model: modelName, Error: fmt.Errorf("failed to create LLM: %w", err)})
				mu.Unlock()
				return nil
			}

			modelForPrompt := ModelProvider(modelName)
			prompt, err := r.promptMgr.Render(CodeReviewPrompt, modelForPrompt, localPromptData)
			if err != nil {
				mu.Lock()
				results = append(results, ComparisonResult{Model: modelName, Error: fmt.Errorf("failed to render prompt: %w", err)})
				mu.Unlock()
				return nil
			}

			// Check context before expensive LLM call
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Use generateWithTimeout to ensure we respect cancellation even if the LLM client hangs
			// Increased to 5m based on logs showing deepseek/kimi taking >2m
			response, err := r.generateWithTimeout(ctx, llmModel, prompt, 5*time.Minute)
			if err != nil {
				mu.Lock()
				// Check if it was a timeout
				if errors.Is(err, context.DeadlineExceeded) {
					results = append(results, ComparisonResult{Model: modelName, Error: fmt.Errorf("generation timed out: %w", err)})
				} else {
					results = append(results, ComparisonResult{Model: modelName, Error: fmt.Errorf("LLM call failed: %w", err)})
				}
				mu.Unlock()
				return nil
			}

			// Check context before saving result
			if ctx.Err() != nil {
				return ctx.Err()
			}

			mu.Lock()
			results = append(results, ComparisonResult{Model: modelName, Review: response})
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return results, nil
}

// generateWithTimeout wraps LLM generation with a hard timeout.
func (r *ragService) generateWithTimeout(ctx context.Context, llm llms.Model, prompt string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		resp string
		err  error
	}
	resultCh := make(chan result, 1)

	go func() {
		resp, err := llm.Call(ctx, prompt)
		select {
		case resultCh <- result{resp, err}:
		case <-ctx.Done():
			// Do not block the goroutine if parent timed out/cancelled
		}
	}()

	select {
	case res := <-resultCh:
		return res.resp, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// GenerateConsensusReview runs a multi-model review and then synthesizes the results into a single consensus review.
//
//nolint:funlen // High-level orchestration function with error handling and artifact saving
func (r *ragService) GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client, models []string) (*core.StructuredReview, error) {
	if repo == nil {
		return nil, errors.New("repo cannot be nil")
	}
	if event == nil {
		return nil, errors.New("event cannot be nil")
	}
	if ghClient == nil {
		return nil, errors.New("ghClient cannot be nil")
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("consensus review requires at least one model")
	}

	// 1. Prepare data (once for all reviews)
	changedFiles, err := ghClient.GetChangedFiles(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files for consensus: %w", err)
	}

	diff, err := ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get diff for consensus: %w", err)
	}

	// 2. Get independent reviews from all models (The "Committee")
	// Deduplicate generator model from comparison to avoid bias (Priority 2)
	generatorModel := r.cfg.AI.GeneratorModel
	var validModels []string
	for _, m := range models {
		if m != generatorModel {
			validModels = append(validModels, m)
		} else {
			r.logger.Info("excluding generator model from comparison (used for synthesis)", "model", m)
		}
	}

	// Ensure we still have enough models
	if len(validModels) < 2 {
		return nil, fmt.Errorf("need at least 2 comparison models after deduplication, got %d", len(validModels))
	}

	comparisonResults, err := r.GenerateComparisonReviews(ctx, repoConfig, repo, event, ghClient, validModels, diff, changedFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to gather consensus reviews: %w", err)
	}

	// 3. Prepare the consensus prompt data & Save Artifacts
	var validReviews []string
	var reviewsBuilder strings.Builder
	// Rob: Nanosecond precision to prevent collisions in fast CI
	timestamp := time.Now().Format("20060102_150405_000000000")
	reviewsDir := "reviews"

	// Security: Verify reviewsDir and resolve symlinks fully (Priority 1)
	absReviewsDir, err := filepath.Abs(reviewsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve reviews dir: %w", err)
	}

	// Resolve all symlinks in path
	resolvedDir, err := filepath.EvalSymlinks(absReviewsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to check reviews directory: %w", err)
	}

	// Get current working directory to validate containment
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}
	absCwd, _ := filepath.Abs(cwd)

	// Verify resolved path is within expected base directory (CWD)
	if resolvedDir != "" {
		rel, err := filepath.Rel(absCwd, resolvedDir)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("reviews directory resolved outside base path")
		}
	}

	// Now safe to create and use
	if err := os.MkdirAll(reviewsDir, 0700); err != nil {
		r.logger.Warn("failed to create reviews directory", "error", err)
	}

	// Deterministic ordering: Sort results by Model name
	sort.Slice(comparisonResults, func(i, j int) bool {
		return comparisonResults[i].Model < comparisonResults[j].Model
	})

	for _, res := range comparisonResults {
		// potential path traversal: strictly replace invalid chars
		sanitizedModel := SanitizeModelForFilename(res.Model)

		if res.Error != nil {
			r.logger.Warn("skipping model due to failure", "model", res.Model, "error", res.Error)
			// Save error artifact for debugging
			errFilename := filepath.Join(reviewsDir, fmt.Sprintf("error_%s_%s.txt", sanitizedModel, timestamp))
			_ = os.WriteFile(errFilename, []byte(res.Error.Error()), 0600) // Security: 0600
			continue
		}

		if strings.TrimSpace(res.Review) == "" {
			continue
		}

		// Save individual review artifact
		filename := filepath.Join(reviewsDir, fmt.Sprintf("review_%s_%s.md", sanitizedModel, timestamp))
		header := fmt.Sprintf("# Code Review by %s\n\n**Date:** %s\n**PR:** %s/%s #%d\n\n", res.Model, time.Now().Format(time.RFC3339), event.RepoOwner, event.RepoName, event.PRNumber)

		// Security: 0600 permissions
		if err := os.WriteFile(filename, []byte(header+res.Review), 0600); err != nil {
			r.logger.Warn("failed to save review artifact", "model", res.Model, "error", err)
		} else {
			r.logger.Info("saved review artifact", "path", filename)
		}

		reviewsBuilder.WriteString(fmt.Sprintf("\n--- Review from %s ---\n", res.Model))
		reviewsBuilder.WriteString(res.Review)
		reviewsBuilder.WriteString("\n")
		validReviews = append(validReviews, res.Model)
	}

	if len(validReviews) == 0 {
		return nil, fmt.Errorf("all models failed to generate valid reviews")
	}

	// Use the same repo configuration as the individual reviews to ensure consistency.
	// While we fetch the changed files again here, this ensures the consensus phase operates
	// on the latest state of the PR.

	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	contextString := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, changedFiles)

	promptData := map[string]string{
		"Reviews":            reviewsBuilder.String(),
		"Context":            contextString,
		"ChangedFiles":       r.formatChangedFiles(changedFiles),
		"CustomInstructions": strings.Join(repoConfig.CustomInstructions, "\n"),
	}

	// 3. Synthesize the final review using the default generator (The "chairperson")
	r.logger.Info("synthesizing consensus review", "models", validReviews)
	rawConsensus, err := r.generateResponseWithPrompt(ctx, event, ConsensusReviewPrompt, promptData)
	if err != nil {
		return nil, fmt.Errorf("failed to generate consensus: %w", err)
	}

	// Save consensus raw artifact
	consensusFilename := filepath.Join(reviewsDir, fmt.Sprintf("review_consensus_%s.md", timestamp))
	if err := os.WriteFile(consensusFilename, []byte(rawConsensus), 0600); err != nil {
		r.logger.Warn("failed to save consensus artifact", "error", err)
	} else {
		r.logger.Info("saved consensus artifact", "path", consensusFilename)
	}

	// 4. Parse JSON
	jsonString, err := r.extractJSON(rawConsensus)
	if err != nil {
		return nil, err
	}

	// Attempt to sanitize JSON (fix common LLM escaping errors like \s in paths)
	jsonString = r.sanitizeJSON(jsonString)

	var structuredReview core.StructuredReview
	if err := json.Unmarshal([]byte(jsonString), &structuredReview); err != nil {
		return nil, fmt.Errorf("failed to parse consensus JSON: %w", err)
	}

	// 5. Add Disclaimer
	disclaimer := fmt.Sprintf("\n\n> 🤖 **AI Consensus Review**\n> Generated by synthesizing findings from: %s. \n> *Mistakes are possible. Please verify critical issues.*", strings.Join(validReviews, ", "))
	structuredReview.Summary += disclaimer

	return &structuredReview, nil
}

func (r *ragService) extractJSON(raw string) (string, error) {
	// 1. Strip Markdown Code Fences (greedy but safe)
	if startFence := strings.Index(raw, "```"); startFence != -1 {
		// Try to find the matching end fence
		if endFence := strings.LastIndex(raw, "```"); endFence > startFence {
			// Extract content inside
			inner := raw[startFence+3 : endFence]
			// Trim language identifier if present (e.g. "json")
			inner = strings.TrimSpace(inner)
			if strings.HasPrefix(strings.ToLower(inner), "json") {
				inner = strings.TrimSpace(inner[4:])
			}
			raw = inner
		}
	}

	raw = strings.TrimSpace(raw)

	// 2. Optimistic attempt: Try to unmarshal the whole thing
	if json.Valid([]byte(raw)) {
		return raw, nil
	}

	// 3. Robust JSON Extraction - handle markdown fences specifically
	// Use a non-greedy approach: Find the first opening fence and the VERY NEXT closing fence.
	startFence := strings.Index(raw, "```")
	if startFence != -1 {
		// Look for the next fence after the opening one
		remaining := raw[startFence+3:]
		endFenceRelative := strings.Index(remaining, "```")
		if endFenceRelative != -1 {
			inner := remaining[:endFenceRelative]
			inner = strings.TrimSpace(inner)
			// Remove optional language identifier
			if strings.HasPrefix(strings.ToLower(inner), "json") {
				inner = strings.TrimSpace(inner[4:])
			}
			raw = inner
		}
	}

	// Now find the first '{' in whatever is left
	startBrace := strings.Index(raw, "{")
	if startBrace == -1 {
		return "", fmt.Errorf("response did not contain valid JSON start")
	}
	raw = raw[startBrace:]

	decoder := json.NewDecoder(strings.NewReader(raw))
	var msg any
	if err := decoder.Decode(&msg); err != nil {
		return "", fmt.Errorf("failed to decode JSON from response: %w", err)
	}
	// Re-encode to get clean, compacted JSON string
	clean, _ := json.Marshal(msg)
	return string(clean), nil
}

// sanitizeJSON attempts to fix common invalid escape sequences in LLM output using round-trip validation.
func (r *ragService) sanitizeJSON(input string) string {
	// 1. Valid JSON? Return as is.
	if json.Valid([]byte(input)) {
		return input
	}

	// 2. Try simple repairs for common LLM mistakes
	var sb strings.Builder
	sb.Grow(len(input) + 20)

	runes := []rune(input)
	length := len(runes)

	for i := 0; i < length; i++ {
		char := runes[i]
		if char == '\\' {
			if i+1 >= length {
				// Trailing backslash - escape it
				sb.WriteRune('\\')
				sb.WriteRune('\\')
				break
			}

			next := runes[i+1]
			switch next {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
				// Valid escape - write both and skip next
				sb.WriteRune(char)
				sb.WriteRune(next)
				i++ // skip next
			default:
				// Invalid escape (e.g. \s in C:\src), escape the backslash
				sb.WriteRune('\\')
				sb.WriteRune('\\')
				// do NOT skip next, let it be processed as normal char
			}
		} else {
			sb.WriteRune(char)
		}
	}

	repaired := sb.String()
	if json.Valid([]byte(repaired)) {
		return repaired
	}

	// 3. Fallback
	return repaired
}

// SanitizeModelForFilename cleans model names for safe use as filenames.
// It handles Windows reserved names and includes a short hash to prevent collisions.
func SanitizeModelForFilename(modelName string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		if r == '-' || r == '.' {
			return r
		}
		return '_'
	}, modelName)

	// De-duplicate underscores
	for strings.Contains(sanitized, "__") {
		sanitized = strings.ReplaceAll(sanitized, "__", "_")
	}

	sanitized = strings.Trim(sanitized, "_")
	if sanitized == "" {
		sanitized = "model"
	}

	// Security: Prevent collisions by adding a short deterministic hash
	h := sha256.New()
	h.Write([]byte(modelName))
	hashStr := hex.EncodeToString(h.Sum(nil))[:8]

	// Windows reserved names check (case-insensitive)
	// Ref: Deepseek review - handle extension-like suffixes (e.g., COM1.txt)
	reserved := map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}

	base := sanitized
	if dot := strings.LastIndex(base, "."); dot > 0 {
		base = base[:dot]
	}

	if reserved[strings.ToUpper(base)] {
		sanitized = "safe_" + sanitized
	}

	// Append hash and limit length
	fullName := sanitized + "_" + hashStr
	if len(fullName) > 120 {
		fullName = fullName[:120]
	}

	return fullName
}

// GenerateReReview now focuses on data preparation and delegates to the helper.
func (r *ragService) GenerateReReview(ctx context.Context, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client) (string, error) {
	r.logger.Info("preparing data for a re-review", "repo", event.RepoFullName, "pr", event.PRNumber)

	newDiff, err := ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return "", fmt.Errorf("failed to get new PR diff: %w", err)
	}
	if strings.TrimSpace(newDiff) == "" {
		r.logger.Info("no new code changes found to re-review", "pr", event.PRNumber)
		return "This pull request contains no new code changes to re-review.", nil
	}

	promptData := core.ReReviewData{
		Language:       event.Language,
		OriginalReview: originalReview.ReviewContent,
		NewDiff:        newDiff,
	}

	return r.generateResponseWithPrompt(ctx, event, ReReviewPrompt, promptData)
}

type QuestionPromptData struct {
	History  string
	Context  string
	Question string
}

type HyDEData struct {
	Patch string
}

func (r *ragService) AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error) {
	r.logger.Info("Answering question with RAG context", "collection", collectionName)

	var relevantDocs []schema.Document
	sparseQuery, err := sparse.GenerateSparseVector(ctx, question)
	if err != nil {
		r.logger.Warn("failed to generate sparse query", "error", err)
		// Fallback to dense-only
		relevantDocs, err = r.vectorStore.SearchCollection(ctx, collectionName, embedderModelName, question, 5)
	} else {
		// Use hybrid search with sparse query
		scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
		relevantDocs, err = scopedStore.SimilaritySearch(ctx, question, 5, vectorstores.WithSparseQuery(sparseQuery))
	}

	for _, doc := range relevantDocs {
		r.logger.Debug("got a document after similarity search:", "document", doc)
	}
	if err != nil {
		return "", fmt.Errorf("failed to perform similarity search: %w", err)
	}
	r.logger.Debug("Retrieved relevant documents for question", "count", len(relevantDocs))

	contextString := r.buildContextForPrompt(relevantDocs)

	promptData := QuestionPromptData{
		Question: question,
		Context:  contextString,
		History:  strings.Join(history, "\n"),
	}
	modelForPrompt := ModelProvider(r.cfg.AI.GeneratorModel)
	prompt, err := r.promptMgr.Render("question", modelForPrompt, promptData)
	if err != nil {
		return "", fmt.Errorf("could not render question prompt: %w", err)
	}

	answer, err := r.generatorLLM.Call(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("LLM call failed for question: %w", err)
	}

	r.logger.Debug("The final LLM answer is", "answer", answer)

	return answer, nil
}

func (r *ragService) buildContextForPrompt(docs []schema.Document) string {
	var contextBuilder strings.Builder
	seenDocs := make(map[string]struct{})

	for _, doc := range docs {
		source, _ := doc.Metadata["source"].(string)
		identifier, _ := doc.Metadata["identifier"].(string)
		parentID, ok := doc.Metadata["parent_id"].(string)
		if !ok {
			parentID = ""
		}

		// Deduplicate based on parent_id if available, otherwise use source + identifier
		docKey := parentID
		if docKey == "" {
			docKey = fmt.Sprintf("%s-%s", source, identifier)
		}

		if _, exists := seenDocs[docKey]; exists {
			continue
		}
		seenDocs[docKey] = struct{}{}

		contextBuilder.WriteString("---\n")
		contextBuilder.WriteString(fmt.Sprintf("File: %s\n", source))

		if pkg, ok := doc.Metadata["package_name"].(string); ok && pkg != "" {
			contextBuilder.WriteString(fmt.Sprintf("Package: %s\n", pkg))
		}

		if identifier != "" && parentID == "" {
			contextBuilder.WriteString(fmt.Sprintf("Identifier: %s\n", identifier))
		}

		contextBuilder.WriteString("\n")
		// Swap snippet with full parent text if available
		content := doc.PageContent
		if parentText, ok := doc.Metadata["full_parent_text"].(string); ok && parentText != "" {
			content = parentText
		}
		contextBuilder.WriteString(content)
		contextBuilder.WriteString("\n---\n\n")
	}
	return contextBuilder.String()
}

func (r *ragService) getOrCreateLLM(modelName string) (llms.Model, error) {
	// For now, just return the initialized generator if model matches or if we don't support dynamic switching yet.
	// This is a simplification to fix the build.
	if modelName == r.cfg.AI.GeneratorModel {
		return r.generatorLLM, nil
	}

	// Create new instance if needed (simplified fallback)
	r.logger.Info("creating new LLM instance on the fly", "model", modelName)
	if r.cfg.AI.LLMProvider == "gemini" {
		return gemini.New(context.Background(), gemini.WithModel(modelName), gemini.WithAPIKey(r.cfg.AI.GeminiAPIKey))
	}
	// Fallback/Default to Ollama
	return ollama.New(
		ollama.WithServerURL(r.cfg.AI.OllamaHost),
		ollama.WithModel(modelName),
	)
}

func (r *ragService) generateResponseWithPrompt(ctx context.Context, event *core.GitHubEvent, promptKey PromptKey, promptData any) (string, error) {
	// Try using the main generator first
	llmModel, err := r.getOrCreateLLM(r.cfg.AI.GeneratorModel)
	if err != nil {
		r.logger.Error("failed to get generator LLM, falling back to legacy config", "error", err)
		// Fallback to legacy if new config fails
		llmModel = r.generatorLLM
	}

	modelForPrompt := ModelProvider(r.cfg.AI.GeneratorModel)
	prompt, err := r.promptMgr.Render(promptKey, modelForPrompt, promptData)
	if err != nil {
		return "", fmt.Errorf("could not render prompt '%s': %w", promptKey, err)
	}

	r.logger.Info("calling LLM for response generation",
		"repo", event.RepoFullName,
		"pr", event.PRNumber,
		"prompt_key", promptKey,
	)

	response, err := llmModel.Call(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("LLM generation failed for prompt '%s': %w", promptKey, err)
	}

	r.logger.Info("LLM response generated successfully", "chars", len(response))
	return response, nil
}

// formatChangedFiles returns a markdown-formatted list of changed file paths
// to include in the LLM prompt.
func (r *ragService) formatChangedFiles(files []internalgithub.ChangedFile) string {
	var builder strings.Builder
	for _, file := range files {
		builder.WriteString(fmt.Sprintf("- `%s`\n", file.Filename))
	}
	return builder.String()
}

// extractSymbolsFromPatch attempts to extract function or type names modified in a patch.
func (r *ragService) extractSymbolsFromPatch(patch string) []string {
	symbols := make(map[string]struct{})
	lines := strings.Split(patch, "\n")

	for _, line := range lines {
		if !strings.HasPrefix(line, "+") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "+"))
		if line == "" {
			continue
		}

		if name := r.matchFuncSymbol(line); name != "" {
			symbols[name] = struct{}{}
		} else if name := r.matchTypeSymbol(line); name != "" {
			symbols[name] = struct{}{}
		}
	}

	result := make([]string, 0, len(symbols))
	for s := range symbols {
		result = append(result, s)
	}
	return result
}

func (r *ragService) matchFuncSymbol(line string) string {
	if !strings.HasPrefix(line, "func ") {
		return ""
	}
	parts := strings.Fields(line)
	for i, part := range parts {
		if part != "func" || i+1 >= len(parts) {
			continue
		}
		name := parts[i+1]
		// Handle receiver: (r *Type) Name
		if strings.HasPrefix(name, "(") {
			for j := i + 1; j < len(parts); j++ {
				if strings.HasSuffix(parts[j], ")") && j+1 < len(parts) {
					name = parts[j+1]
					break
				}
			}
		}
		// Strip params and generics
		if idx := strings.IndexAny(name, "(["); idx != -1 {
			name = name[:idx]
		}
		return strings.TrimSpace(name)
	}
	return ""
}

func (r *ragService) matchTypeSymbol(line string) string {
	if !strings.HasPrefix(line, "type ") {
		return ""
	}
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// buildRelevantContext performs similarity searches using file diffs to find related
// code snippets from the repository. These results provide context to help the LLM
// better understand the scope and impact of the changes. Duplicate entries are avoided.
// It also fetches architectural summaries for the affected directories.
func (r *ragService) buildRelevantContext(ctx context.Context, collectionName, embedderModelName string, changedFiles []internalgithub.ChangedFile) string {
	if len(changedFiles) == 0 {
		return ""
	}

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
	var seenDocsMu sync.RWMutex
	seenDocs := make(map[string]struct{})

	// Run context gathering in parallel for lower latency
	var wg sync.WaitGroup
	var archContext, impactContext string
	var hydeMap map[int]string
	var hydeResults [][]schema.Document
	var indices []int

	// 1. Architectural Context
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.logger.Info("stage started", "name", "ArchitecturalContext")
		// Sec: Passing seenDocsMu to protect seenDocs map if getArchContext ever writes to it
		if ac := r.getArchContext(ctx, scopedStore, changedFiles); ac != "" {
			archContext = ac
		}
		r.logger.Info("stage completed", "name", "ArchitecturalContext")
	}()

	// 2. HyDE Snippets (Optional: High Latency, High Recall)
	if r.cfg.AI.EnableHyDE {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.logger.Info("stage started", "name", "HyDE")
			hydeMap = r.generateHyDESnippets(ctx, changedFiles)
			hydeResults, indices = r.searchHyDEBatch(ctx, collectionName, embedderModelName, changedFiles, hydeMap)
			r.logger.Info("stage completed", "name", "HyDE", "snippets_generated", len(hydeMap))
		}()
	} else {
		r.logger.Info("stage skipped", "name", "HyDE", "reason", "disabled_in_config")
	}

	// 3. Impact Context
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.logger.Info("stage started", "name", "ImpactAnalysis")
		// Sec: Pass the shared seenDocs and its mutex to avoid cross-stage duplicates safely
		if ic := r.getImpactContext(ctx, scopedStore, changedFiles, seenDocs, &seenDocsMu); ic != "" {
			impactContext = ic
		}
		r.logger.Info("stage completed", "name", "ImpactAnalysis")
	}()

	wg.Wait()

	var contextBuilder strings.Builder

	// Assemble Architectural Context
	if archContext != "" {
		contextBuilder.WriteString("# Architectural Context\n\n")
		contextBuilder.WriteString("The following describes the purpose of the affected modules:\n\n")
		contextBuilder.WriteString(archContext)
		contextBuilder.WriteString("\n---\n\n")
	}

	// Assemble Impact Context
	if impactContext != "" {
		r.logger.Info("impact analysis identified potential ripple effects", "context_length", len(impactContext))
		contextBuilder.WriteString("# Potential Impacted Callers & Usages\n\n")
		contextBuilder.WriteString("The following code snippets may be affected by the changes in modified symbols:\n\n")
		contextBuilder.WriteString(impactContext)
		contextBuilder.WriteString("\n---\n\n")
	}

	// Assemble Related Snippets (minor overlap with Impact is acceptable for speed)
	seenDocsMu.RLock()
	relatedSnippets := r.formatRelatedSnippets(hydeResults, indices, changedFiles, seenDocs, &seenDocsMu)
	seenDocsMu.RUnlock()
	if relatedSnippets != "" {
		contextBuilder.WriteString("# Related Code Snippets\n\n")
		contextBuilder.WriteString(relatedSnippets)
	}

	r.logger.Info("relevant context built",
		"changed_files", len(changedFiles),
		"hyde_snippets", len(hydeMap),
		"arch_len", len(archContext),
	)

	return contextBuilder.String()
}

func (r *ragService) formatRelatedSnippets(hydeResults [][]schema.Document, indices []int, changedFiles []internalgithub.ChangedFile, seenDocs map[string]struct{}, seenMu *sync.RWMutex) string {
	var builder strings.Builder
	var topFiles []string

	for i, docs := range hydeResults {
		originalFile := changedFiles[indices[i]]
		for j, doc := range docs {
			topFiles = r.processRelatedSnippet(doc, originalFile, j, seenDocs, seenMu, topFiles, &builder)
		}
	}

	if len(topFiles) > 0 {
		r.logger.Info("HyDE search results", "top_files", topFiles)
	}
	return builder.String()
}

func (r *ragService) processRelatedSnippet(doc schema.Document, originalFile internalgithub.ChangedFile, rank int, seenDocs map[string]struct{}, seenMu *sync.RWMutex, topFiles []string, builder *strings.Builder) []string {
	source, _ := doc.Metadata["source"].(string)
	if source == "" || r.isArchDocument(doc) {
		return topFiles
	}

	parentID, ok := doc.Metadata["parent_id"].(string)
	if !ok {
		parentID = ""
	}
	docKey := parentID
	if docKey == "" {
		docKey = source
	}

	seenMu.RLock()
	_, exists := seenDocs[docKey]
	seenMu.RUnlock()

	if !exists {
		if len(topFiles) < 3 {
			topFiles = append(topFiles, source)
		}
		// Swap snippet with full parent text if available
		content := doc.PageContent
		if parentText, ok := doc.Metadata["full_parent_text"].(string); ok && parentText != "" {
			content = parentText
		}
		fmt.Fprintf(builder, "**%s** (relevant to %s):\n```\n%s\n```\n\n",
			source, originalFile.Filename, content)
		seenDocs[docKey] = struct{}{}
	}

	// Fallback: even if we've seen it, if it's top result for another file, it's worth noting in debug logs
	if rank == 0 && len(topFiles) < 3 {
		alreadyLogged := false
		for _, f := range topFiles {
			if f == source {
				alreadyLogged = true
				break
			}
		}
		if !alreadyLogged {
			topFiles = append(topFiles, source)
		}
	}
	return topFiles
}

func (r *ragService) getArchContext(ctx context.Context, scopedStore storage.ScopedVectorStore, files []internalgithub.ChangedFile) string {
	filePaths := make([]string, len(files))
	for i, f := range files {
		filePaths[i] = f.Filename
	}
	archContext, err := r.GetArchContextForPaths(ctx, scopedStore, filePaths)
	if err != nil {
		r.logger.Warn("failed to get architectural context", "error", err)
		return ""
	}
	if archContext != "" {
		r.logger.Debug("retrieved architectural context", "folders_count", len(filePaths))
	}
	return archContext
}

func (r *ragService) generateHyDESnippets(ctx context.Context, files []internalgithub.ChangedFile) map[int]string {
	type hydeResult struct {
		index   int
		snippet string
	}
	hydeChan := make(chan hydeResult, len(files))
	var wg sync.WaitGroup
	// Semaphore to limit concurrent LLM calls (e.g., 5 at a time)
	sem := make(chan struct{}, 10)
	defer close(sem)
	cacheHits := 0

	for i, file := range files {
		if file.Patch == "" {
			continue
		}

		// Compute patch hash for cache lookup
		patchHash := r.hashPatch(file.Patch)

		// Check cache first
		if cached, ok := r.hydeCache.Load(patchHash); ok {
			if snippet, valid := cached.(string); valid {
				hydeChan <- hydeResult{index: i, snippet: snippet}
				cacheHits++
				continue
			}
		}

		wg.Add(1)
		go func(idx int, f internalgithub.ChangedFile, hash string) {
			defer wg.Done()

			// Con: Select-based acquisition to respect context cancellation
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			prompt, err := r.promptMgr.Render(HyDEPrompt, DefaultProvider, HyDEData{Patch: f.Patch})
			if err != nil {
				return
			}
			snippet, _ := llms.GenerateFromSinglePrompt(ctx, r.generatorLLM, prompt)
			if snippet != "" {
				r.hydeCache.Store(hash, snippet) // Cache for future use
				hydeChan <- hydeResult{index: idx, snippet: snippet}
			}
		}(i, file, patchHash)
	}

	go func() {
		wg.Wait()
		close(hydeChan)
	}()

	hydeMap := make(map[int]string)
	for res := range hydeChan {
		hydeMap[res.index] = res.snippet
	}

	if cacheHits > 0 {
		r.logger.Debug("hyde cache hits", "hits", cacheHits, "total", len(files))
	}
	return hydeMap
}

// hashPatch computes a short hash of the patch content for caching.
func (r *ragService) hashPatch(patch string) string {
	hash := sha256.Sum256([]byte(patch))
	return hex.EncodeToString(hash[:8])
}

//nolint:gocognit,nestif // Complex hybrid search logic with multiple fallbacks
func (r *ragService) searchHyDEBatch(ctx context.Context, collectionName, embedderModelName string, files []internalgithub.ChangedFile, hydeMap map[int]string) ([][]schema.Document, []int) {
	queries := make([]string, 0, len(files)*2)
	indices := make([]int, 0, len(files)*2)

	for i, file := range files {
		if file.Patch == "" {
			continue
		}
		queries = append(queries, fmt.Sprintf(
			"To understand the impact of changes in the file '%s', find relevant code that interacts with or is related to the following diff:\n%s",
			file.Filename, file.Patch,
		))
		indices = append(indices, i)

		if snippet, ok := hydeMap[i]; ok {
			queries = append(queries, snippet)
			indices = append(indices, i)
		}
	}

	if len(queries) == 0 {
		return nil, nil
	}

	// Two-stage retrieval:
	// 1. Recall: Fetch more docs than needed (e.g. 20)
	// 2. Precision: Rerank and keep top K (e.g. 5)

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
	finalResults := make([][]schema.Document, len(queries))

	// We process queries sequentially or could parallelize locally, but for now sequentially is safer for logic correctness
	// The underlying Reranker might have concurrency.

	// Create a base retriever for recall.
	// We need 20 documents for recall.
	// Note: vectorstores.ToRetriever might not be available or might be a simple wrapper.
	// If ToRetriever is not available, we can just use scopedStore.SimilaritySearch directly in a custom Retriever or just inline.
	// But let's follow the user guide which suggested `vectorstores.ToRetriever`.
	// If it fails compile, I will fix.

	// baseRetriever was used here but now we use SCOPED store directly.

	for i, query := range queries {
		// Generate sparse vector for query if possible
		var searchOpts []vectorstores.Option
		sparseVec, err := sparse.GenerateSparseVector(ctx, query)
		if err == nil {
			searchOpts = append(searchOpts, vectorstores.WithSparseQuery(sparseVec))
		}

		// We need to use scopedStore directly to pass options, bypassing ToRetriever if we want hybrid search.
		// However, RerankingRetriever expects a Retriever interface.
		// For now, let's just use scopedStore for the base retrieval MANUALLY instead of baseRetriever.

		baseDocs, err := scopedStore.SimilaritySearch(ctx, query, 20, searchOpts...)
		if err != nil {
			r.logger.Warn("base hybrid search failed", "error", err)
			continue
		}
		r.logger.Info("retrieval stats", "step", "pre-rerank", "query_idx", i, "doc_count", len(baseDocs))

		// Now rerank manually
		var docs []schema.Document
		scoredDocs, err := r.reranker.Rerank(ctx, query, baseDocs)
		if err != nil {
			r.logger.Warn("reranking failed for query, falling back to base results", "error", err, "query_idx", i)
			if len(baseDocs) > 5 {
				docs = baseDocs[:5]
			} else {
				docs = baseDocs
			}
		} else {
			// Convert ScoredDocument to Document and slice top 5
			// Assuming Reranker sorts by score descending
			count := len(scoredDocs)
			if count > 5 {
				count = 5
			}
			docs = make([]schema.Document, count)
			for j := range count {
				// We might want to preserve the score in metadata if needed
				docs[j] = scoredDocs[j].Document
				if docs[j].Metadata == nil {
					docs[j].Metadata = make(map[string]any)
				}
				docs[j].Metadata["score"] = scoredDocs[j].Score
				docs[j].Metadata["rerank_reason"] = scoredDocs[j].Reason
			}
		}
		r.logger.Info("retrieval stats", "step", "post-rerank", "query_idx", i, "doc_count", len(docs))
		finalResults[i] = docs
	}

	return finalResults, indices
}

//nolint:gocognit
func (r *ragService) getImpactContext(ctx context.Context, scopedStore storage.ScopedVectorStore, files []internalgithub.ChangedFile, seenDocs map[string]struct{}, seenMu *sync.RWMutex) string {
	symbols := make(map[string]struct{})
	for _, file := range files {
		if file.Patch == "" {
			continue
		}
		extracted := r.extractSymbolsFromPatch(file.Patch)
		if len(extracted) > 0 {
			r.logger.Debug("extracted symbols for impact analysis", "file", file.Filename, "symbols", extracted)
		}
		for _, sym := range extracted {
			symbols[sym] = struct{}{}
		}
	}

	if len(symbols) == 0 {
		return ""
	}

	var symbolList []string
	for s := range symbols {
		symbolList = append(symbolList, s)
	}

	query := fmt.Sprintf("Find code that calls or uses the following symbols: %s", strings.Join(symbolList, ", "))
	docs, err := scopedStore.SimilaritySearch(ctx, query, 8)
	if err != nil {
		r.logger.Warn("failed to fetch impact context", "error", err)
		return ""
	}

	var builder strings.Builder
	for _, doc := range docs {
		source, _ := doc.Metadata["source"].(string)
		parentID, ok := doc.Metadata["parent_id"].(string)
		if !ok {
			parentID = ""
		}
		docKey := parentID
		if docKey == "" {
			docKey = source
		}

		seenMu.RLock()
		_, exists := seenDocs[docKey]
		seenMu.RUnlock()

		if exists || r.isArchDocument(doc) {
			continue
		}

		// Swap snippet with full parent text if available
		content := doc.PageContent
		if parentText, ok := doc.Metadata["full_parent_text"].(string); ok && parentText != "" {
			content = parentText
		}

		builder.WriteString(fmt.Sprintf("**%s** (potential impact usage):\n```\n%s\n```\n\n",
			source, content))
		seenMu.Lock()
		seenDocs[docKey] = struct{}{}
		seenMu.Unlock()
	}
	return builder.String()
}

func (r *ragService) isArchDocument(doc schema.Document) bool {
	ct, ok := doc.Metadata["chunk_type"].(string)
	return ok && ct == "arch"
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
		// Normalize the file path - remove any leading separators and clean it
		cleanFile := filepath.Clean(strings.TrimPrefix(file, string(filepath.Separator)))

		isExcluded := false
		for _, excludeDir := range excludeDirs {
			cleanExcludeDir := filepath.Clean(excludeDir)

			// Check if the file path is exactly the excluded directory
			if cleanFile == cleanExcludeDir {
				isExcluded = true
				break
			}

			// Check if the file path starts with the excluded directory followed by a separator
			if strings.HasPrefix(cleanFile, cleanExcludeDir+string(filepath.Separator)) {
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
