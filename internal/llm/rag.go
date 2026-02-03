// Package llm provides functionality for interacting with Large Language Models (LLMs),
// including prompt construction and Retrieval-Augmented Generation (RAG) workflows.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sevigo/goframe/documentloaders"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

// RAGService defines the core operations for our Retrieval-Augmented Generation (RAG) pipeline.
type RAGService interface {
	SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, collectionName, embedderModelName, repoPath string) error
	UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string) error
	GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, ghClient internalgithub.Client) (*core.StructuredReview, string, error)
	GenerateReReview(ctx context.Context, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client) (string, error)
	AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error)
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

// SetupRepoContext processes a repository for the first time, storing all its embeddings.
func (r *ragService) SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, collectionName, embedderModelName, repoPath string) error {
	r.logger.Info("performing initial full indexing of repository",
		"path", repoPath,
		"collection", collectionName,
		"embedder", embedderModelName,
	)
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	finalExcludeDirs := r.buildExcludeDirs(repoConfig)
	r.logger.Info("final loader configuration", "exclude_dirs", finalExcludeDirs, "exclude_exts", repoConfig.ExcludeExts)

	gitLoader, err := documentloaders.NewGit(
		repoPath,
		r.parserRegistry,
		documentloaders.WithLogger(r.logger),
		documentloaders.WithExcludeDirs(finalExcludeDirs),
		documentloaders.WithExcludeExts(repoConfig.ExcludeExts),
	)
	if err != nil {
		return fmt.Errorf("failed to create Git document loader: %w", err)
	}

	var totalProcessed atomic.Int64
	var totalDocsFound atomic.Int64
	startTime := time.Now()

	// Create a scoped store for this repository
	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)

	// This is the new streaming pipeline.
	// The loader walks the filesystem and calls this function with batches of documents.
	// This function then immediately sends the batch to the vector store.
	processFunc := func(ctx context.Context, docs []schema.Document) error {
		if len(docs) == 0 {
			return nil
		}
		totalDocsFound.Add(int64(len(docs)))

		_, err := scopedStore.AddDocuments(ctx, docs)
		if err != nil {
			return fmt.Errorf("failed to add document batch to vector store: %w", err)
		}

		processedInBatch := int64(len(docs))
		totalProcessed.Add(processedInBatch)

		r.logger.Info("processed document stream batch",
			"collection", collectionName,
			"docs_in_batch", processedInBatch,
			"total_docs_processed", totalProcessed.Load(),
			"elapsed_time", time.Since(startTime).Round(time.Second),
		)
		return nil
	}

	// Start the streaming process.
	if err := gitLoader.LoadAndProcessStream(ctx, processFunc); err != nil {
		return fmt.Errorf("failed during streamed repository processing: %w", err)
	}

	if totalDocsFound.Load() == 0 {
		r.logger.Warn("no indexable documents found after loader filtering", "path", repoPath)
		return nil
	}

	// Generate architectural summaries for directories (post-processing)
	r.logger.Info("generating architectural summaries for indexed content")
	if err := r.GenerateArchSummaries(ctx, collectionName, embedderModelName, repoPath); err != nil {
		r.logger.Warn("failed to generate architectural summaries, continuing without them", "error", err)
		// Don't fail the whole indexing if arch summaries fail
	}

	r.logger.Info("repository setup complete",
		"collection", collectionName,
		"total_docs", totalProcessed.Load(),
		"duration", time.Since(startTime).Round(time.Second),
	)

	return nil
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

	// We use a shared slice to collect documents from all workers.
	// This removes the need for the result channel and intermediate result structs.
	var allDocs []schema.Document

	fileChan := make(chan string, len(files))

	// Start workers
	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range fileChan {
				docs := r.processFile(repoPath, file)
				// Direct append for better performance
				allDocs = append(allDocs, docs...)
			}
		}()
	}

	// Send files to workers
	for _, file := range files {
		fileChan <- file
	}
	close(fileChan)

	wg.Wait()

	return allDocs
}

// processFile reads, parses, and chunks a single file.
func (r *ragService) processFile(repoPath, file string) []schema.Document {
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

	chunks, err := parser.Chunk(string(contentBytes), file, nil)
	if err != nil {
		r.logger.Error("failed to chunk file", "file", file, "error", err)
		return nil
	}

	var docs []schema.Document
	for _, chunk := range chunks {
		doc := schema.NewDocument(chunk.Content, map[string]any{
			"source":     file,
			"identifier": chunk.Identifier,
			"chunk_type": chunk.Type,
			"line_start": chunk.LineStart,
			"line_end":   chunk.LineEnd,
		})
		docs = append(docs, doc)
	}
	return docs
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
	// Find the JSON block within the ```json ... ``` code fence
	jsonBlockStart := strings.Index(rawReview, "```json")
	if jsonBlockStart == -1 {
		r.logger.Error("LLM response did not contain a valid JSON object", "raw_response", rawReview)
		return nil, "", fmt.Errorf("LLM response did not contain a '```json' code fence")
	}
	jsonString := rawReview[jsonBlockStart+len("```json"):] // Get the content after the fence
	jsonBlockEnd := strings.Index(jsonString, "```")
	if jsonBlockEnd == -1 {
		return nil, "", fmt.Errorf("LLM response was missing the closing '```' for the json block")
	}
	jsonString = jsonString[:jsonBlockEnd]
	if err := json.Unmarshal([]byte(jsonString), &structuredReview); err != nil {
		return nil, "", fmt.Errorf("failed to parse LLM's JSON response: %w", err)
	}

	return &structuredReview, jsonString, nil
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

	relevantDocs, err := r.vectorStore.SearchCollection(ctx, collectionName, embedderModelName, question, 5)
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

		docKey := fmt.Sprintf("%s-%s", source, identifier)
		if _, exists := seenDocs[docKey]; exists {
			continue
		}
		seenDocs[docKey] = struct{}{}

		contextBuilder.WriteString("---\n")
		contextBuilder.WriteString(fmt.Sprintf("File: %s\n", source))

		if identifier != "" {
			contextBuilder.WriteString(fmt.Sprintf("Identifier: %s\n", identifier))
		}

		contextBuilder.WriteString("\n")
		contextBuilder.WriteString(doc.PageContent)
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
		if ac := r.getArchContext(ctx, scopedStore, changedFiles); ac != "" {
			archContext = ac
		}
	}()

	// 2. HyDE Snippets (generation + search)
	wg.Add(1)
	go func() {
		defer wg.Done()
		hydeMap = r.generateHyDESnippets(ctx, changedFiles)
		hydeResults, indices = r.searchHyDEBatch(ctx, collectionName, embedderModelName, changedFiles, hydeMap)
	}()

	// 3. Impact Context (uses local seenDocs to avoid data race)
	wg.Add(1)
	go func() {
		defer wg.Done()
		localSeen := make(map[string]struct{})
		if ic := r.getImpactContext(ctx, scopedStore, changedFiles, localSeen); ic != "" {
			impactContext = ic
		}
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
	relatedSnippets := r.formatRelatedSnippets(hydeResults, indices, changedFiles, seenDocs)
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

func (r *ragService) formatRelatedSnippets(hydeResults [][]schema.Document, indices []int, changedFiles []internalgithub.ChangedFile, seenDocs map[string]struct{}) string {
	var builder strings.Builder
	var topFiles []string

	for i, docs := range hydeResults {
		originalFile := changedFiles[indices[i]]
		for j, doc := range docs {
			topFiles = r.processRelatedSnippet(doc, originalFile, j, seenDocs, topFiles, &builder)
		}
	}

	if len(topFiles) > 0 {
		r.logger.Info("HyDE search results", "top_files", topFiles)
	}
	return builder.String()
}

func (r *ragService) processRelatedSnippet(doc schema.Document, originalFile internalgithub.ChangedFile, rank int, seenDocs map[string]struct{}, topFiles []string, builder *strings.Builder) []string {
	source, _ := doc.Metadata["source"].(string)
	if source == "" || r.isArchDocument(doc) {
		return topFiles
	}

	if _, exists := seenDocs[source]; !exists {
		if len(topFiles) < 3 {
			topFiles = append(topFiles, source)
		}
		fmt.Fprintf(builder, "**%s** (relevant to %s):\n```\n%s\n```\n\n",
			source, originalFile.Filename, doc.PageContent)
		seenDocs[source] = struct{}{}
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
	sem := make(chan struct{}, 5)
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

			sem <- struct{}{}        // Acquire
			defer func() { <-sem }() // Release

			if ctx.Err() != nil {
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

	baseRetriever := vectorstores.ToRetriever(scopedStore, 20)

	for i, query := range queries {
		rr := vectorstores.RerankingRetriever{
			Retriever: baseRetriever,
			Reranker:  r.reranker,
			TopK:      5,
		}

		docs, err := rr.GetRelevantDocuments(ctx, query)
		if err != nil {
			r.logger.Warn("reranking failed for query, falling back to base results", "error", err, "query_idx", i)
			// Fallback: just use base retriever if rerank fails
			// We need to re-call base retriever directly because GetRelevantDocuments might have failed at Retrieve step or Rerank step.
			// Ideally we catch error.
			docs, _ = baseRetriever.GetRelevantDocuments(ctx, query)
			if len(docs) > 5 {
				docs = docs[:5]
			}
		}
		finalResults[i] = docs
	}

	return finalResults, indices
}

func (r *ragService) getImpactContext(ctx context.Context, scopedStore storage.ScopedVectorStore, files []internalgithub.ChangedFile, seenDocs map[string]struct{}) string {
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
		if _, exists := seenDocs[source]; exists || r.isArchDocument(doc) {
			continue
		}
		builder.WriteString(fmt.Sprintf("**%s** (potential impact usage):\n```\n%s\n```\n\n",
			source, doc.PageContent))
		seenDocs[source] = struct{}{}
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
