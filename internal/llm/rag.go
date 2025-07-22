// Package llm provides functionality for interacting with Large Language Models (LLMs),
// including prompt construction and Retrieval-Augmented Generation (RAG) workflows.
package llm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sevigo/goframe/documentloaders"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

// RAGService defines the core operations for our Retrieval-Augmented Generation (RAG) pipeline.
type RAGService interface {
	SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, collectionName, repoPath string) error
	UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, collectionName, repoPath string, filesToProcess, filesToDelete []string) error
	GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, collectionName string, event *core.GitHubEvent, ghClient internalgithub.Client) (string, error)
	GenerateReReview(ctx context.Context, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client) (string, error)
}

type ragService struct {
	cfg            *config.Config
	promptMgr      *PromptManager
	vectorStore    storage.VectorStore
	generatorLLM   llms.Model
	parserRegistry parsers.ParserRegistry
	logger         *slog.Logger
}

// NewRAGService creates a new RAGService instance with a vector store, LLM model,
// parser registry, and logger. This service powers the indexing and code review flow.
func NewRAGService(
	cfg *config.Config,
	promptMgr *PromptManager,
	vs storage.VectorStore,
	gen llms.Model,
	pr parsers.ParserRegistry,
	logger *slog.Logger,
) RAGService {
	return &ragService{
		cfg:            cfg,
		promptMgr:      promptMgr,
		vectorStore:    vs,
		generatorLLM:   gen,
		parserRegistry: pr,
		logger:         logger,
	}
}

// SetupRepoContext processes a repository for the first time, storing all its embeddings.
func (r *ragService) SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, collectionName, repoPath string) error {
	r.logger.Info("performing initial full indexing of repository", "path", repoPath, "collection", collectionName)

	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	finalExcludeDirs := r.buildExcludeDirs(repoConfig)

	r.logger.Info("final loader configuration", "exclude_dirs", finalExcludeDirs, "exclude_exts", repoConfig.ExcludeExts)

	gitLoader := documentloaders.NewGit(
		repoPath,
		r.parserRegistry,
		documentloaders.WithLogger(r.logger),
		documentloaders.WithExcludeDirs(finalExcludeDirs),
		documentloaders.WithExcludeExts(repoConfig.ExcludeExts),
	)

	docs, err := gitLoader.Load(ctx)
	if err != nil {
		return fmt.Errorf("failed to load repository documents: %w", err)
	}

	if len(docs) == 0 {
		r.logger.Warn("no indexable documents found after loader filtering", "path", repoPath)
		return nil
	}

	r.logger.Info("storing initial documents in vector database", "collection", collectionName, "doc_count", len(docs))
	return r.vectorStore.AddDocuments(ctx, collectionName, docs)
}

// UpdateRepoContext incrementally updates the vector store based on file changes.
func (r *ragService) UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, collectionName, repoPath string, filesToProcess, filesToDelete []string) error {
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
		"collection", collectionName,
		"process", len(filesToProcess),
		"delete", len(filesToDelete),
		"exclude_dirs", finalExcludeDirs,
		"exclude_exts", repoConfig.ExcludeExts)

	// Handle deleted files first
	if len(filesToDelete) > 0 {
		r.logger.Info("deleting embeddings for removed files", "count", len(filesToDelete))
		if err := r.vectorStore.DeleteDocuments(ctx, collectionName, filesToDelete); err != nil {
			r.logger.Error("failed to delete some embeddings", "error", err)
		}
	}

	// Handle added and modified files
	if len(filesToProcess) == 0 {
		return nil
	}

	var allDocs []schema.Document
	for _, file := range filesToProcess {
		fullPath := filepath.Join(repoPath, file)
		contentBytes, err := os.ReadFile(fullPath)
		if err != nil {
			r.logger.Error("failed to read file for update, skipping", "file", file, "error", err)
			continue
		}

		parser, err := r.parserRegistry.GetParserForFile(fullPath, nil)
		if err != nil {
			r.logger.Warn("no suitable parser found for file, skipping", "file", file, "error", err)
			continue
		}

		chunks, err := parser.Chunk(string(contentBytes), file, nil)
		if err != nil {
			r.logger.Error("failed to chunk file", "file", file, "error", err)
			continue
		}

		for _, chunk := range chunks {
			doc := schema.NewDocument(chunk.Content, map[string]any{
				"source":     file,
				"identifier": chunk.Identifier,
				"chunk_type": chunk.Type,
				"line_start": chunk.LineStart,
				"line_end":   chunk.LineEnd,
			})
			allDocs = append(allDocs, doc)
		}
	}

	if len(allDocs) > 0 {
		r.logger.Info("adding/updating documents in vector store", "count", len(allDocs))
		if err := r.vectorStore.AddDocuments(ctx, collectionName, allDocs); err != nil {
			return fmt.Errorf("failed to add/update embeddings for changed files: %w", err)
		}
	}

	return nil
}

// GenerateReview now focuses on data preparation and delegates to the helper.
func (r *ragService) GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, collectionName string, event *core.GitHubEvent, ghClient internalgithub.Client) (string, error) {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	r.logger.Info("preparing data for a full review", "repo", event.RepoFullName, "pr", event.PRNumber)

	diff, err := ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return "", fmt.Errorf("failed to get PR diff: %w", err)
	}
	if diff == "" {
		r.logger.Info("no code changes in pull request", "pr", event.PRNumber)
		return "This pull request contains no code changes. LGTM!", nil
	}

	changedFiles, err := ghClient.GetChangedFiles(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		r.logger.Warn("could not retrieve changed files list", "error", err)
	}

	r.buildRelevantContext(ctx, collectionName, changedFiles)

	contextString, err := r.buildRelevantContext(ctx, collectionName, changedFiles)
	if err != nil {
		return "", err
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

	return r.generateResponseWithPrompt(ctx, event, CodeReviewPrompt, promptData)
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

func (r *ragService) generateResponseWithPrompt(ctx context.Context, event *core.GitHubEvent, promptKey PromptKey, promptData any) (string, error) {
	modelForPrompt := ModelProvider(r.cfg.GeneratorModelName)
	prompt, err := r.promptMgr.Render(promptKey, modelForPrompt, promptData)
	if err != nil {
		return "", fmt.Errorf("could not render prompt '%s': %w", promptKey, err)
	}

	r.logger.Info("calling LLM for response generation",
		"repo", event.RepoFullName,
		"pr", event.PRNumber,
		"prompt_key", promptKey,
	)

	response, err := r.generatorLLM.Call(ctx, prompt)
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
func (r *ragService) buildRelevantContext(ctx context.Context, collectionName string, changedFiles []internalgithub.ChangedFile) (string, error) {
	if len(changedFiles) == 0 {
		return "", nil
	}

	// Prepare a batch of queries and corresponding original files.
	// This new slice will maintain the correct mapping between queries/results and the original files.
	queries := make([]string, 0, len(changedFiles))
	originalFilesForQueries := make([]internalgithub.ChangedFile, 0, len(changedFiles))

	for _, file := range changedFiles {
		if file.Patch == "" {
			continue // Skip files without a patch, as no query can be formed.
		}
		query := fmt.Sprintf(
			"To understand the impact of changes in the file '%s', find relevant code that interacts with or is related to the following diff:\n%s",
			file.Filename,
			file.Patch,
		)
		queries = append(queries, query)
		originalFilesForQueries = append(originalFilesForQueries, file) // Store the file for this specific query
	}

	// If no queries were generated (e.g., all files had empty patches), return early.
	if len(queries) == 0 {
		return "", nil
	}

	// Execute the batch search in a single network call.
	batchResults, err := r.vectorStore.SimilaritySearchBatch(ctx, collectionName, queries, 3)
	if err != nil {
		r.logger.Error("failed to retrieve RAG context in batch operation; LLM will proceed without relevant code snippets", "error", err)
		return "", fmt.Errorf("failed to retrieve RAG context: %w", err)
	}

	// Process the results, mapping them back to the original files.
	var contextBuilder strings.Builder
	seenDocs := make(map[string]struct{})

	// Sanity check: Ensure the number of results matches the number of queries.
	// This should hold true if the vector store contract is respected.
	if len(batchResults) != len(originalFilesForQueries) {
		r.logger.Error("mismatch between batch results and original files list; context attribution may be incorrect",
			"batch_results_count", len(batchResults),
			"expected_files_count", len(originalFilesForQueries))
		// Decide on a more robust fallback here if this state is possible and needs different handling.
		return "", fmt.Errorf("mismatch between batch results and original files list")
	}

	for i, relevantDocs := range batchResults {
		// Use the correctly mapped original file for this batch result.
		originalFile := originalFilesForQueries[i]

		for _, doc := range relevantDocs {
			source, ok := doc.Metadata["source"].(string)
			if !ok {
				contentPreview := doc.PageContent
				if len(contentPreview) > 50 {
					contentPreview = contentPreview[:50] + "..."
				}
				r.logger.Debug("document missing 'source' metadata or it's not a string, skipping",
					"document_content_preview", contentPreview)
				continue
			}

			if _, exists := seenDocs[source]; !exists {
				contextBuilder.WriteString(fmt.Sprintf("**%s** (relevant to %s):\n```\n%s\n```\n\n",
					source, originalFile.Filename, doc.PageContent))
				seenDocs[source] = struct{}{}
			}
		}
	}
	return contextBuilder.String(), nil
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
