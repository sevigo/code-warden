// Package llm provides functionality for interacting with Large Language Models (LLMs),
// including prompt construction and Retrieval-Augmented Generation (RAG) workflows.
package llm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sevigo/goframe/documentloaders"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/parsers"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

// RAGService defines the core operations for our Retrieval-Augmented Generation (RAG) pipeline.
type RAGService interface {
	SetupRepoContext(ctx context.Context, collectionName, repoPath string) error
	GenerateReview(ctx context.Context, collectionName string, event *core.GitHubEvent, ghClient internalgithub.Client) (string, error)
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

// SetupRepoContext processes a repository and stores its embeddings into the vector store.
func (r *ragService) SetupRepoContext(ctx context.Context, collectionName, repoPath string) error {
	r.logger.Info("indexing repository for code review context", "path", repoPath, "collection", collectionName)

	gitLoader := documentloaders.NewGit(
		repoPath,
		r.parserRegistry,
		documentloaders.WithLogger(r.logger),
		documentloaders.WithExcludeDirs([]string{".git", ".github", "vendor", "node_modules", "target", "build"}),
	)

	docs, err := gitLoader.Load(ctx)
	if err != nil {
		return fmt.Errorf("failed to load repository documents: %w", err)
	}

	if len(docs) == 0 {
		r.logger.Warn("no indexable documents found", "path", repoPath)
		return nil
	}

	r.logger.Info("storing documents in vector database", "collection", collectionName, "doc_count", len(docs))
	return r.vectorStore.AddDocuments(ctx, collectionName, docs)
}

// GenerateReview executes the full RAG pipeline to generate a code review for a pull request.
// It retrieves the PR diff, identifies changed files, fetches relevant code context
// using vector search, constructs a prompt, and queries the LLM to produce the review.
func (r *ragService) GenerateReview(ctx context.Context, collectionName string, event *core.GitHubEvent, ghClient internalgithub.Client) (string, error) {
	r.logger.Info("generating code review", "repo", event.RepoFullName, "pr", event.PRNumber)

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

	changedFilesList := r.formatChangedFiles(changedFiles)
	contextContent := r.buildRelevantContext(ctx, collectionName, changedFiles)

	promptData := map[string]string{
		"Title":        event.PRTitle,
		"Description":  event.PRBody,
		"Language":     event.Language,
		"ChangedFiles": changedFilesList,
		"Context":      contextContent,
		"Diff":         diff,
	}

	// The GeneratorModelName from configuration (e.g., "gemini-2.5-flash")
	// is used as the ModelProvider to fetch model-specific prompts.
	// Prompt filenames should follow the convention `[prompt_key]_[model_provider].prompt`
	// or fall back to `[prompt_key]_default.prompt`.
	modelForPrompt := ModelProvider(r.cfg.GeneratorModelName)
	prompt, err := r.promptMgr.Render(
		CodeReviewPrompt,
		modelForPrompt,
		promptData,
	)
	if err != nil {
		r.logger.Error("could not retrieve prompt from the manager", "error", err)
		return "", err
	}

	r.logger.Info("calling LLM for review generation",
		"repo", event.RepoFullName,
		"pr", event.PRNumber,
	)

	review, err := r.generatorLLM.Call(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("LLM review generation failed: %w", err)
	}

	r.logger.Info("code review generated successfully", "review_chars", len(review))
	return review, nil
}

// GenerateReReview creates a prompt with the original review and new diff, then calls the LLM.
func (r *ragService) GenerateReReview(ctx context.Context, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client) (string, error) {
	r.logger.Info("generating re-review", "repo", event.RepoFullName, "pr", event.PRNumber)

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

	modelForPrompt := ModelProvider(r.cfg.GeneratorModelName)
	prompt, err := r.promptMgr.Render(ReReviewPrompt, modelForPrompt, promptData)
	if err != nil {
		return "", fmt.Errorf("could not render re-review prompt: %w", err)
	}

	r.logger.Info("calling LLM for re-review generation", "pr", event.PRNumber)

	// 5. Call the LLM to get the follow-up review.
	review, err := r.generatorLLM.Call(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("LLM re-review generation failed: %w", err)
	}

	r.logger.Info("re-review generated successfully", "review_chars", len(review))
	return review, nil
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
func (r *ragService) buildRelevantContext(ctx context.Context, collectionName string, changedFiles []internalgithub.ChangedFile) string {
	var contextBuilder strings.Builder
	seenDocs := make(map[string]struct{})

	for _, file := range changedFiles {
		r.logger.Debug("searching for relevant context", "file", file.Filename)

		query := fmt.Sprintf(
			"To understand the impact of changes in the file '%s', find relevant code that interacts with or is related to the following diff:\n%s",
			file.Filename,
			file.Patch,
		)
		relevantDocs, err := r.vectorStore.SimilaritySearch(ctx, collectionName, query, 3)
		if err != nil {
			r.logger.Warn("context search failed", "file", file.Filename, "error", err)
			continue
		}

		for _, doc := range relevantDocs {
			if source, ok := doc.Metadata["source"].(string); ok {
				if _, exists := seenDocs[source]; !exists {
					contextBuilder.WriteString(fmt.Sprintf("**%s** (relevant to %s):\n```\n%s\n```\n\n",
						source, file.Filename, doc.PageContent))
					seenDocs[source] = struct{}{}
				}
			}
		}
	}

	return contextBuilder.String()
}
