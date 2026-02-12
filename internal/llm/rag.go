// Package llm provides functionality for interacting with Large Language Models (LLMs),
// including prompt construction and Retrieval-Augmented Generation (RAG) workflows.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/sevigo/goframe/documentloaders"
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
	GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	GenerateReReview(ctx context.Context, repo *storage.Repository, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client, changedFiles []internalgithub.ChangedFile) (string, error)
	AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error)
	ProcessFile(repoPath, file string) []schema.Document
	GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error)
	GenerateComparisonReviews(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client, models []string, preFetchedDiff string, preFetchedFiles []internalgithub.ChangedFile, preComputedContext string) ([]ComparisonResult, error)
	GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, models []string, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	GetTextSplitter() textsplitter.TextSplitter
}

type ragService struct {
	cfg            *config.Config
	promptMgr      *PromptManager
	vectorStore    storage.VectorStore
	store          storage.Store
	generatorLLM   llms.Model
	reranker       schema.Reranker
	parserRegistry parsers.ParserRegistry
	splitter       textsplitter.TextSplitter
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
	splitter textsplitter.TextSplitter,
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
		splitter:       splitter,
		logger:         logger,
	}
}

func (r *ragService) GetTextSplitter() textsplitter.TextSplitter {
	return r.splitter
}

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
			mu.Lock()
			processedCount += len(docsByFile) // Approximate by file count
			mu.Unlock()

			// Update repository tracking in DB
			if err := r.store.UpsertFiles(ctx, repo.ID, filesToUpdate); err != nil {
				r.logger.Error("failed to update file state in DB", "error", err)
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("repository ingestion failed: %w", err)
	}

	// Cleanup: Delete records for files that are genuinely absent from disk.
	// We check the filesystem directly rather than relying on filesProcessedByLoader,
	// because the loader intentionally skips generated files (mocks, protobuf, etc.)
	// and we don't want to delete their tracking records.
	var pathsToDelete []string
	for path := range existingFiles {
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
				docs := r.ProcessFile(repoPath, f)
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
func (r *ragService) ProcessFile(repoPath, file string) []schema.Document {
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

	splitDocs, err := r.splitter.SplitDocuments(context.Background(), []schema.Document{doc})
	if err != nil {
		r.logger.Error("failed to split document with code-aware splitter", "file", file, "error", err)
		return nil
	}

	for i := range splitDocs {
		// Ensure sparse vectors are generated for hybrid search if possible
		sparseVec, err := sparse.GenerateSparseVector(context.Background(), splitDocs[i].PageContent)
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

// GenerateReview builds the review using pre-fetched diff and changed files.
func (r *ragService) GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	r.logger.Info("preparing data for a full review", "repo", event.RepoFullName, "pr", event.PRNumber, "embedder", repo.EmbedderModelName)
	if diff == "" {
		r.logger.Info("no code changes in pull request", "pr", event.PRNumber)
		noChangesReview := &core.StructuredReview{
			Summary:     "This pull request contains no code changes. Looks good to me!",
			Suggestions: []core.Suggestion{},
		}
		return noChangesReview, noChangesReview.Summary, nil
	}

	contextString := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

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
		return nil, err.Error(), err
	}

	// Parse Markdown Review
	structuredReview, err := parseMarkdownReview(rawReview)
	if err != nil {
		r.logger.Warn("failed to parse markdown review, using raw output as fallback", "error", err)
		// Fallback: Use raw output as summary
		structuredReview = &core.StructuredReview{
			Summary: rawReview,
		}
	}

	if structuredReview.Verdict == "" {
		structuredReview.Verdict = "COMMENT" // Default if missing
	}
	return structuredReview, rawReview, nil
}

// GenerateComparisonReviews calculates common context once and performs final analysis with multiple models.
func (r *ragService) GenerateComparisonReviews(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client, models []string, preFetchedDiff string, preFetchedFiles []internalgithub.ChangedFile, preComputedContext string) ([]ComparisonResult, error) {
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

	contextString := preComputedContext
	if contextString == "" {
		contextString = r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)
	}

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

	resultsChan := make(chan ComparisonResult, len(models))
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Limit concurrency to avoid rate limits
	const maxConcurrentModels = 5
	sem := make(chan struct{}, maxConcurrentModels)

	for _, modelName := range models {
		r.spawnReviewWorker(workerCtx, modelName, promptData, sem, resultsChan)
	}

	results, err := r.waitForQuorumResults(ctx, models, resultsChan)
	if err != nil {
		return nil, err
	}

	return results, nil
}

//nolint:gocognit // Worker coordination with semaphore, context, and cleanup is inherently complex
func (r *ragService) spawnReviewWorker(ctx context.Context, m string, promptData map[string]string, sem chan struct{}, resultsChan chan<- ComparisonResult) {
	go func() {
		result := ComparisonResult{Model: m}
		sent := false
		defer func() {
			if !sent {
				select {
				case resultsChan <- result:
				case <-time.After(100 * time.Millisecond):
					// Prevent indefinite block if collector already exited (though unlikely given the design)
				}
			}
		}()

		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return
		default:
		}

		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			result.Error = ctx.Err()
			return
		}

		localPromptData := make(map[string]string, len(promptData))
		for k, v := range promptData {
			localPromptData[k] = v
		}

		llmModel, err := r.getOrCreateLLM(m)
		if err != nil {
			result.Error = fmt.Errorf("failed to create LLM: %w", err)
			return
		}

		modelForPrompt := ModelProvider(m)
		prompt, err := r.promptMgr.Render(CodeReviewPrompt, modelForPrompt, localPromptData)
		if err != nil {
			result.Error = fmt.Errorf("failed to render prompt: %w", err)
			return
		}

		response, err := r.generateWithTimeout(ctx, llmModel, prompt, 5*time.Minute)
		if err != nil {
			result.Error = fmt.Errorf("generation failed: %w", err)
			return
		}

		result.Review = response

		select {
		case resultsChan <- result:
			sent = true
		case <-ctx.Done():
			result.Error = ctx.Err()
		}
	}()
}

func (r *ragService) waitForQuorumResults(ctx context.Context, models []string, resultsChan <-chan ComparisonResult) ([]ComparisonResult, error) {
	results := make([]ComparisonResult, 0, len(models))
	// Use ceiling division to ensure for N=2 we wait for 2, not 1.
	// (N*2 + 2) / 3 implements ceil(N*2/3) using integer arithmetic.
	quorumThreshold := (len(models)*2 + 2) / 3
	if quorumThreshold < 1 {
		quorumThreshold = 1
	}

	quorumTimer := time.NewTimer(24 * time.Hour)
	quorumTimer.Stop()
	quorumTimerStarted := false

	for range models {
		select {
		case res := <-resultsChan:
			results = append(results, res)
			if len(results) >= quorumThreshold && !quorumTimerStarted && len(results) < len(models) {
				const stragglerTimeout = 30 * time.Second
				quorumTimer.Reset(stragglerTimeout)
				quorumTimerStarted = true
			}
		case <-quorumTimer.C:
			return results, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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

func (r *ragService) GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, models []string, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}
	if err := r.validateConsensusParams(repo, event, models); err != nil {
		return nil, "", err
	}

	// 1. Get independent reviews from all models (The "Committee")
	if len(models) < 1 {
		return nil, "", fmt.Errorf("need at least 1 comparison model, got %d", len(models))
	}

	// 3. Centralized Context Building (The "Context Foundation")
	contextString := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

	comparisonResults, err := r.GenerateComparisonReviews(ctx, repoConfig, repo, event, nil, models, diff, changedFiles, contextString)
	if err != nil {
		return nil, "", fmt.Errorf("failed to gather consensus reviews: %w", err)
	}

	// 4. Synthesize the final review
	rawConsensus, validReviews, err := r.synthesizeConsensus(ctx, repoConfig, event, comparisonResults, contextString, changedFiles)
	if err != nil {
		return nil, "", err
	}

	// 5. Parse and Add Disclaimer
	structuredReview, err := parseMarkdownReview(rawConsensus)
	if err != nil {
		r.logger.Warn("failed to parse consensus review, using raw output as fallback", "error", err)
		structuredReview = &core.StructuredReview{Summary: rawConsensus}
	}

	disclaimer := fmt.Sprintf("\n\n> 🤖 **AI Consensus Review**\n> Generated by synthesizing findings from: %s. \n> *Mistakes are possible. Please verify critical issues.*", strings.Join(validReviews, ", "))
	structuredReview.Summary += disclaimer

	return structuredReview, rawConsensus, nil
}

func (r *ragService) validateConsensusParams(repo *storage.Repository, event *core.GitHubEvent, models []string) error {
	if repo == nil {
		return errors.New("repo cannot be nil")
	}
	if event == nil {
		return errors.New("event cannot be nil")
	}
	if len(models) == 0 {
		return fmt.Errorf("consensus review requires at least one model")
	}
	return nil
}

func (r *ragService) synthesizeConsensus(ctx context.Context, repoConfig *core.RepoConfig, event *core.GitHubEvent, results []ComparisonResult, context string, changedFiles []internalgithub.ChangedFile) (string, []string, error) {
	var validReviews []string
	var reviewsBuilder strings.Builder
	timestamp := time.Now().Format("20060102_150405_000000000")
	reviewsDir := "reviews"

	// Resolve artifacts directory safely
	if err := r.ensureReviewsDir(reviewsDir); err != nil {
		return "", nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Model < results[j].Model
	})

	for _, res := range results {
		if res.Error != nil || strings.TrimSpace(res.Review) == "" {
			continue
		}
		r.saveReviewArtifact(reviewsDir, res, event, timestamp)
		reviewsBuilder.WriteString(fmt.Sprintf("\n--- Review from %s ---\n", res.Model))
		reviewsBuilder.WriteString(res.Review)
		reviewsBuilder.WriteString("\n")
		validReviews = append(validReviews, res.Model)
	}

	if len(validReviews) == 0 {
		return "", nil, fmt.Errorf("all models failed to generate valid reviews")
	}

	promptData := map[string]string{
		"Reviews":            reviewsBuilder.String(),
		"Context":            context,
		"ChangedFiles":       r.formatChangedFiles(changedFiles),
		"CustomInstructions": strings.Join(repoConfig.CustomInstructions, "\n"),
	}

	rawConsensus, err := r.generateResponseWithPrompt(ctx, event, ConsensusReviewPrompt, promptData)
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate consensus: %w", err)
	}

	r.saveConsensusArtifact(reviewsDir, rawConsensus, timestamp)
	return rawConsensus, validReviews, nil
}

func (r *ragService) ensureReviewsDir(reviewsDir string) error {
	absReviewsDir, err := filepath.Abs(reviewsDir)
	if err != nil {
		return fmt.Errorf("failed to resolve reviews dir: %w", err)
	}

	resolvedDir, err := filepath.EvalSymlinks(absReviewsDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to check reviews directory: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}
	absCwd, _ := filepath.Abs(cwd)

	if resolvedDir != "" {
		rel, err := filepath.Rel(absCwd, resolvedDir)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("reviews directory resolved outside base path")
		}
	}

	if err := os.MkdirAll(reviewsDir, 0700); err != nil {
		r.logger.Warn("failed to create reviews directory", "error", err)
	}
	return nil
}

func (r *ragService) saveReviewArtifact(dir string, res ComparisonResult, event *core.GitHubEvent, ts string) {
	sanitizedModel := SanitizeModelForFilename(res.Model)
	filename := filepath.Join(dir, fmt.Sprintf("review_%s_%s.md", sanitizedModel, ts))
	header := fmt.Sprintf("# Code Review by %s\n\n**Date:** %s\n**PR:** %s/%s #%d\n\n", res.Model, time.Now().Format(time.RFC3339), event.RepoOwner, event.RepoName, event.PRNumber)
	if err := os.WriteFile(filename, []byte(header+res.Review), 0600); err != nil {
		r.logger.Warn("failed to save review artifact", "model", res.Model, "error", err)
	}
}

func (r *ragService) saveConsensusArtifact(dir, raw, ts string) {
	filename := filepath.Join(dir, fmt.Sprintf("review_consensus_%s.md", ts))
	if err := os.WriteFile(filename, []byte(raw), 0600); err != nil {
		r.logger.Warn("failed to save consensus artifact", "error", err)
	}
}

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
	hashStr := hex.EncodeToString(h.Sum(nil))[:16]

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
func (r *ragService) GenerateReReview(ctx context.Context, repo *storage.Repository, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client, changedFiles []internalgithub.ChangedFile) (string, error) {
	r.logger.Info("preparing data for a re-review", "repo", event.RepoFullName, "pr", event.PRNumber)

	newDiff, err := ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return "", fmt.Errorf("failed to get new PR diff: %w", err)
	}
	if strings.TrimSpace(newDiff) == "" {
		r.logger.Info("no new code changes found to re-review", "pr", event.PRNumber)
		return "This pull request contains no new code changes to re-review.", nil
	}

	// Build context (Arch + Impact + HyDE) just like a full review
	contextString := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

	promptData := core.ReReviewData{
		Language:         event.Language,
		OriginalReview:   originalReview.ReviewContent,
		NewDiff:          newDiff,
		UserInstructions: event.UserInstructions,
		Context:          contextString,
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

// buildRelevantContext performs similarity searches using file diffs to find related
// code snippets from the repository. These results provide context to help the LLM
// better understand the scope and impact of the changes. Duplicate entries are avoided.
// It also fetches architectural summaries for the affected directories.
func (r *ragService) buildRelevantContext(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prDescription string) string {
	if len(changedFiles) == 0 {
		return ""
	}

	// Bound the number of files processed to prevent OOM/DoS
	const defaultMaxContextFiles = 20
	if len(changedFiles) > defaultMaxContextFiles {
		r.logger.Warn("truncating context files", "total", len(changedFiles), "limit", defaultMaxContextFiles)
		changedFiles = changedFiles[:defaultMaxContextFiles]
	}

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
	var seenDocsMu sync.RWMutex
	seenDocs := make(map[string]struct{})

	// Run context gathering in parallel for lower latency
	var wg sync.WaitGroup
	var archContext, impactContext, descriptionContext string
	var hydeResults [][]schema.Document
	var indices []int

	// 1. Architectural Context
	wg.Add(1)
	go func() {
		defer wg.Done()
		archContext = r.gatherArchContext(ctx, scopedStore, changedFiles)
	}()

	// 2. HyDE Snippets
	if r.cfg.AI.EnableHyDE {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hydeResults, indices = r.gatherHyDEContext(ctx, collectionName, embedderModelName, changedFiles)
		}()
	} else {
		r.logger.Info("stage skipped", "name", "HyDE", "reason", "disabled_in_config")
	}

	// 3. Impact Context
	wg.Add(1)
	go func() {
		defer wg.Done()
		impactContext = r.gatherImpactContext(ctx, scopedStore, repoPath, changedFiles, seenDocs, &seenDocsMu)
	}()

	// 4. Description Context (MultiQuery)
	if prDescription != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			descriptionContext = r.gatherDescriptionContext(ctx, collectionName, embedderModelName, prDescription)
		}()
	}

	wg.Wait()

	// Assemble and return the combined context from all stages.
	// Validation happens per-snippet inside gatherDescriptionContext via validateSnippetRelevance.
	return r.assembleContext(archContext, impactContext, descriptionContext, hydeResults, indices, changedFiles, seenDocs, &seenDocsMu)
}

// gatherDescriptionContext uses MultiQuery retrieval to find code related to the PR description.
// It generates 3 query variations via a small LLM, searches for each, deduplicates results,
// and validates each snippet's relevance before including it.
func (r *ragService) gatherDescriptionContext(ctx context.Context, collection, embedder, description string) string {
	r.logger.Info("stage started", "name", "DescriptionContext")

	scopedStore := r.vectorStore.ForRepo(collection, embedder)

	// Use a fast model for generating query variations
	queryLLM, err := r.getOrCreateLLM(r.cfg.AI.FastModel)
	if err != nil {
		queryLLM = r.generatorLLM
	}

	// Generate 3 search queries from the PR description for broader recall
	prompt := fmt.Sprintf("Generate 3 different search queries to find code relevant to this PR description:\n%s\nReturn only the queries, one per line.", description)
	variationResp, err := queryLLM.Call(ctx, prompt)
	if err != nil {
		r.logger.Warn("query variation generation failed, falling back to raw description", "error", err)
		variationResp = description
	}
	variations := strings.Split(variationResp, "\n")

	var allDocs []schema.Document
	for _, q := range variations {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		docs, err := scopedStore.SimilaritySearch(ctx, q, 3)
		if err != nil {
			r.logger.Warn("similarity search failed for query variation", "query", q, "error", err)
			continue
		}
		allDocs = append(allDocs, docs...)
	}

	// Deduplicate by content hash
	uniqueDocs := make(map[string]schema.Document)
	for _, d := range allDocs {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(d.PageContent)))
		uniqueDocs[hash] = d
	}

	var builder strings.Builder
	if len(uniqueDocs) > 0 {
		builder.WriteString("# Related to PR Description\n\n")

		// Validate snippets in parallel to avoid sequential LLM latency
		type validatedSnippet struct {
			text string
		}
		var validated []validatedSnippet
		var validMu sync.Mutex
		var valWg sync.WaitGroup

		for _, d := range uniqueDocs {
			valWg.Add(1)
			go func() {
				defer valWg.Done()
				if r.validateSnippetRelevance(ctx, d.PageContent, description) {
					snip := fmt.Sprintf("File: %s\n```\n%s\n```\n\n", d.Metadata["source"], d.PageContent)
					validMu.Lock()
					validated = append(validated, validatedSnippet{text: snip})
					validMu.Unlock()
				}
			}()
		}
		valWg.Wait()

		for _, v := range validated {
			builder.WriteString(v.text)
		}
	}

	r.logger.Info("stage completed", "name", "DescriptionContext", "unique_snippets", len(uniqueDocs))
	return builder.String()
}

// validateSnippetRelevance uses a fast LLM to check if a retrieved snippet
// is actually relevant to the given context. Fails open (returns true) if
// the validator model is unavailable or returns an error.
func (r *ragService) validateSnippetRelevance(ctx context.Context, snippet, prContext string) bool {
	validatorLLM, err := r.getOrCreateLLM(r.cfg.AI.FastModel)
	if err != nil {
		return true // Fail open: if no validator available, include the snippet
	}

	prompt := fmt.Sprintf("Is the following code snippet relevant to this context?\nContext: %s\nSnippet:\n%s\n\nReply with YES or NO.", prContext, snippet)
	resp, err := validatorLLM.Call(ctx, prompt)
	if err != nil {
		return true
	}
	return strings.Contains(strings.ToUpper(resp), "YES")
}

func (r *ragService) gatherArchContext(ctx context.Context, store storage.ScopedVectorStore, files []internalgithub.ChangedFile) string {
	r.logger.Info("stage started", "name", "ArchitecturalContext")
	ac := r.getArchContext(ctx, store, files)
	r.logger.Info("stage completed", "name", "ArchitecturalContext")
	return ac
}

const hydeBaseQueryPrompt = "To understand the impact of changes in the file '%s', find relevant code that interacts with or is related to the following diff:\n%s"

func (r *ragService) gatherHyDEContext(ctx context.Context, collection, embedder string, files []internalgithub.ChangedFile) ([][]schema.Document, []int) {
	r.logger.Info("stage started", "name", "HyDE")

	workChan := make(chan struct {
		originalIdx int
		query       string
	}, len(files)*2)
	resultsChan := make(chan struct {
		idx  int
		docs []schema.Document
	}, len(files)*2)

	var searchWg sync.WaitGroup
	var genWg sync.WaitGroup

	// 1. Start Search Workers
	scopedStore := r.vectorStore.ForRepo(collection, embedder)
	for range 3 {
		searchWg.Add(1)
		go func() {
			defer searchWg.Done()
			for work := range workChan {
				docs := r.performSingleHyDEJob(ctx, scopedStore, work.query)
				if len(docs) > 0 {
					resultsChan <- struct {
						idx  int
						docs []schema.Document
					}{work.originalIdx, docs}
				}
			}
		}()
	}

	// 2. Start Generator
	go r.runHyDEGenerator(ctx, files, &genWg, workChan)

	// 3. Collector (waits for workers)
	go func() {
		searchWg.Wait()
		close(resultsChan)
	}()

	return r.collectHyDEResults(ctx, resultsChan)
}

func (r *ragService) runHyDEGenerator(ctx context.Context, files []internalgithub.ChangedFile, wg *sync.WaitGroup, workChan chan<- struct {
	originalIdx int
	query       string
}) {
	defer close(workChan)

	maxConcurrency := r.cfg.AI.HyDEConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 5
	}
	hydeSem := make(chan struct{}, maxConcurrency)
	for i, file := range files {
		if file.Patch == "" {
			continue
		}

		// Queue Base Query IMMEDIATELY
		baseQuery := fmt.Sprintf(hydeBaseQueryPrompt, file.Filename, file.Patch)
		workChan <- struct {
			originalIdx int
			query       string
		}{originalIdx: i, query: baseQuery}

		// Queue HyDE Snippet Generation (Async)
		if isLogicFile(file.Filename) {
			wg.Add(1)
			go func(idx int, f internalgithub.ChangedFile) {
				defer wg.Done()
				snippet := r.generateSingleHyDESnippet(ctx, f, hydeSem)
				if snippet != "" {
					workChan <- struct {
						originalIdx int
						query       string
					}{originalIdx: idx, query: snippet}
				}
			}(i, file)
		}
	}
	wg.Wait()
}

func (r *ragService) collectHyDEResults(ctx context.Context, resultsChan <-chan struct {
	idx  int
	docs []schema.Document
}) ([][]schema.Document, []int) {
	var finalResults [][]schema.Document
	var finalIndices []int

	for {
		select {
		case res, ok := <-resultsChan:
			if !ok {
				r.logger.Info("HyDE collection completed", "queries_processed", len(finalResults))
				return finalResults, finalIndices
			}
			finalResults = append(finalResults, res.docs)
			finalIndices = append(finalIndices, res.idx)
		case <-ctx.Done():
			r.logger.Warn("HyDE collection cancelled", "error", ctx.Err())
			return finalResults, finalIndices
		}
	}
}

func (r *ragService) performSingleHyDEJob(ctx context.Context, scopedStore storage.ScopedVectorStore, query string) []schema.Document {
	// 1. Clean the Query (Bottleneck #5: Strip diff noise)
	cleanQuery := stripPatchNoise(query)

	var searchOpts []vectorstores.Option
	// Key Change: Use un-stripped query for Sparse Vector to capture exact CamelCase identifiers
	// that might be present in the diff metadata or deleted lines.
	sparseVec, err := sparse.GenerateSparseVector(ctx, query)
	if err == nil {
		searchOpts = append(searchOpts, vectorstores.WithSparseQuery(sparseVec))
	}

	// Recall
	baseDocs, err := scopedStore.SimilaritySearch(ctx, cleanQuery, 20, searchOpts...)
	if err != nil {
		r.logger.Warn("base hybrid search failed", "error", err)
		return nil
	}

	// 2. Pre-filter by Keyword Score (Bottleneck #2: N+1 Reranking)
	preFilteredDocs := preFilterBM25(cleanQuery, baseDocs, 10)

	// Precision (Rerank)
	scoredDocs, err := r.reranker.Rerank(ctx, cleanQuery, preFilteredDocs)
	if err != nil {
		r.logger.Warn("reranking failed, falling back", "error", err)
		return r.fallbackDocs(preFilteredDocs, 5)
	}

	return r.formatScoredDocs(scoredDocs, 5)
}

func (r *ragService) fallbackDocs(docs []schema.Document, limit int) []schema.Document {
	if len(docs) > limit {
		return docs[:limit]
	}
	return docs
}

func (r *ragService) formatScoredDocs(scoredDocs []schema.ScoredDocument, limit int) []schema.Document {
	count := len(scoredDocs)
	if count > limit {
		count = limit
	}
	docs := make([]schema.Document, count)
	for j := range count {
		docs[j] = scoredDocs[j].Document
		if docs[j].Metadata == nil {
			docs[j].Metadata = make(map[string]any)
		}
		docs[j].Metadata["score"] = scoredDocs[j].Score
		docs[j].Metadata["rerank_reason"] = scoredDocs[j].Reason
	}
	return docs
}

func (r *ragService) generateSingleHyDESnippet(ctx context.Context, file internalgithub.ChangedFile, sem chan struct{}) string {
	patchHash := r.hashPatch(file.Patch)
	if cached, ok := r.hydeCache.Load(patchHash); ok {
		if snippet, valid := cached.(string); valid {
			return snippet
		}
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return ""
	}

	prompt, err := r.promptMgr.Render(HyDEPrompt, DefaultProvider, HyDEData{Patch: file.Patch})
	if err != nil {
		r.logger.Error("failed to render HyDE prompt", "error", err, "file", file.Filename)
		return ""
	}

	snippet, _ := llms.GenerateFromSinglePrompt(ctx, r.generatorLLM, prompt)
	if snippet != "" {
		r.hydeCache.Store(patchHash, snippet)
	} else {
		r.logger.Error("HyDE generation returned empty result", "file", file.Filename, "patchHash", patchHash)
	}
	return snippet
}

func (r *ragService) gatherImpactContext(ctx context.Context, store storage.ScopedVectorStore, repoPath string, files []internalgithub.ChangedFile, seen map[string]struct{}, mu *sync.RWMutex) string {
	r.logger.Info("stage started", "name", "ImpactAnalysis")
	ic := r.getImpactContext(ctx, store, repoPath, files, seen, mu)
	r.logger.Info("stage completed", "name", "ImpactAnalysis")
	return ic
}

func (r *ragService) assembleContext(arch, impact, description string, hyde [][]schema.Document, indices []int, files []internalgithub.ChangedFile, seen map[string]struct{}, mu *sync.RWMutex) string {
	var contextBuilder strings.Builder

	if arch != "" {
		contextBuilder.WriteString("# Architectural Context\n\n")
		contextBuilder.WriteString("The following describes the purpose of the affected modules:\n\n")
		contextBuilder.WriteString(arch)
		contextBuilder.WriteString("\n---\n\n")
	}

	if description != "" {
		contextBuilder.WriteString(description) // Already formatted in gatherDescriptionContext
		contextBuilder.WriteString("\n---\n\n")
	}

	if impact != "" {
		r.logger.Info("impact analysis identified potential ripple effects", "context_length", len(impact))
		contextBuilder.WriteString("# Potential Impacted Callers & Usages\n\n")
		contextBuilder.WriteString("The following code snippets may be affected by the changes in modified symbols:\n\n")
		contextBuilder.WriteString(impact)
		contextBuilder.WriteString("\n---\n\n")
	}

	if len(hyde) > 0 {
		contextBuilder.WriteString("# Related Code Snippets\n\n")
		contextBuilder.WriteString("The following code snippets might be relevant to the changes being reviewed:\n\n")

		mu.Lock()
		defer mu.Unlock()

		for i, docs := range hyde {
			if i >= len(indices) { // Safety check
				continue
			}
			originalIdx := indices[i]
			if originalIdx >= len(files) { // Safety check
				continue
			}
			filePath := files[originalIdx].Filename
			for _, doc := range docs {
				// Use a content hash for deduplication to avoid adding the same snippet multiple times
				// even if it comes from different HyDE queries.
				contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(doc.PageContent)))
				if _, exists := seen[contentHash]; exists {
					continue
				}
				seen[contentHash] = struct{}{}

				contextBuilder.WriteString(fmt.Sprintf("## Related to: %s\n", filePath))
				contextBuilder.WriteString("```\n")
				contextBuilder.WriteString(doc.PageContent)
				contextBuilder.WriteString("\n```\n\n")
			}
		}
	}

	r.logger.Info("relevant context built",
		"changed_files", len(files),
		"arch_len", len(arch),
		"impact_len", len(impact),
		"hyde_results_count", len(hyde),
	)

	return contextBuilder.String()
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

		seenMu.Lock()
		seenDocs[docKey] = struct{}{}
		seenMu.Unlock()
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

// hashPatch computes a short hash of the patch content for caching.
func (r *ragService) hashPatch(patch string) string {
	hash := sha256.Sum256([]byte(patch))
	return hex.EncodeToString(hash[:8])
}

// isLogicFile returns true if the file is a likely code/logic file.
func isLogicFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return isCodeExtension(ext)
}

func (r *ragService) getImpactContext(ctx context.Context, store storage.ScopedVectorStore, repoPath string, files []internalgithub.ChangedFile, seen map[string]struct{}, mu *sync.RWMutex) string {
	retriever := vectorstores.NewDependencyRetriever(store)
	var impactBuilder strings.Builder

	for _, file := range files {
		parser, err := r.parserRegistry.GetParserForFile(file.Filename, nil)
		if err != nil {
			continue
		}

		// Read actual source from disk — parsing git patch syntax would fail
		fullPath, err := r.validateAndJoinPath(repoPath, file.Filename)
		if err != nil {
			continue
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		meta, err := parser.ExtractMetadata(string(content), file.Filename)
		if err != nil {
			continue
		}

		network, err := retriever.GetContextNetwork(ctx, meta.PackageName, meta.Imports)
		if err != nil {
			r.logger.Warn("dependency retrieval failed", "file", file.Filename, "error", err)
			continue
		}

		// Process dependents (impact analysis)
		for _, doc := range network.Dependents {
			source, ok := doc.Metadata["source"].(string)
			if !ok || source == "" {
				continue
			}
			mu.Lock()
			if _, exists := seen[source]; exists {
				mu.Unlock()
				continue
			}
			seen[source] = struct{}{}
			mu.Unlock()

			_, _ = impactBuilder.WriteString(fmt.Sprintf("File: %s (potential ripple effect from %s)\n---\n%s\n\n",
				source, file.Filename, doc.PageContent))
		}
	}
	return impactBuilder.String()
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

// stripPatchNoise removes git metadata and deleted lines, preserving additions and context for semantic search.
func stripPatchNoise(query string) string {
	if query == "" {
		return ""
	}
	lines := strings.Split(query, "\n")
	var cleanLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "diff --git"):
			continue
		case strings.HasPrefix(trimmed, "index "):
			continue
		case strings.HasPrefix(trimmed, "new file mode"):
			continue
		case strings.HasPrefix(trimmed, "deleted file mode"):
			continue
		case strings.HasPrefix(trimmed, "--- "), strings.HasPrefix(trimmed, "+++ "), strings.HasPrefix(trimmed, "@@"):
			continue // Strip diff headers
		case strings.HasPrefix(trimmed, "-"):
			continue // Skip deleted lines
		case strings.HasPrefix(trimmed, "+"):
			// Preserve additions with their + prefix so the LLM recognizes them as new code
			cleanLines = append(cleanLines, line)
		default:
			if trimmed != "" {
				cleanLines = append(cleanLines, line) // Preserve context and HyDE preamble
			}
		}
	}
	if len(cleanLines) == 0 {
		return ""
	}
	return strings.Join(cleanLines, "\n")
}

// preFilterBM25 performs a simple keyword-overlap based ranking to trim results
// before sending them to the expensive reranker.
func preFilterBM25(query string, docs []schema.Document, topK int) []schema.Document {
	if len(docs) <= topK {
		return docs
	}

	type scoredDoc struct {
		doc   schema.Document
		score int
	}

	// Simple keyword overlap score
	queryTerms := strings.Fields(strings.ToLower(query))
	filteredTerms := make([]string, 0, len(queryTerms))
	for _, t := range queryTerms {
		if len(t) >= 3 {
			filteredTerms = append(filteredTerms, t)
		}
	}

	if len(filteredTerms) == 0 {
		return docs
	}

	scored := make([]scoredDoc, len(docs))
	for i, doc := range docs {
		score := 0
		content := strings.ToLower(doc.PageContent)
		for _, term := range filteredTerms {
			if strings.Contains(content, term) {
				score++
			}
		}
		scored[i] = scoredDoc{doc: doc, score: score}
	}

	// Sort by overlap score
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	result := make([]schema.Document, topK)
	for i := range topK {
		result[i] = scored[i].doc
	}
	return result
}
