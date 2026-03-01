package review

import (
	"context"
	"fmt"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/prompts"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// GenerateReview generates a structured code review using the RAG pipeline.
func (s *Service) GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	s.cfg.Logger.Info("preparing data for a full review", "repo", event.RepoFullName, "pr", event.PRNumber, "embedder", repo.EmbedderModelName)
	if diff == "" {
		s.cfg.Logger.Info("no code changes in pull request", "pr", event.PRNumber)
		noChangesReview := &core.StructuredReview{
			Summary:     "This pull request contains no code changes. Looks good to me!",
			Suggestions: []core.Suggestion{},
		}
		return noChangesReview, noChangesReview.Summary, nil
	}

	// If changedFiles is empty (internal review), extract them from the diff
	if len(changedFiles) == 0 {
		changedFiles = ParseDiff(diff)
		s.cfg.Logger.Info("extracted changed files from diff for internal review", "count", len(changedFiles))
	}

	// Get context
	contextString, definitionsContext := s.cfg.BuildContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

	// Check for empty context to warn about hallucination risk
	contextEmpty := contextIsEmpty(contextString, definitionsContext)
	if contextEmpty {
		s.cfg.Logger.Warn("HIGH HALLUCINATION RISK: no context retrieved from vector store - review will be based solely on diff without repository context",
			"repo", event.RepoFullName,
			"pr", event.PRNumber,
			"changed_files", len(changedFiles),
		)
		// Inject warning messages into context for the LLM
		contextString = "**WARNING: No repository context available. Review based solely on the provided diff. Do not assume external code structure.**"
		definitionsContext = "**WARNING: No type definitions resolved. Verify types are defined outside this diff.**"
	}

	promptData := s.buildReviewPromptData(event, repoConfig, contextString, definitionsContext, diff, changedFiles)

	promptStr, err := s.cfg.PromptMgr.Render(llm.CodeReviewPrompt, promptData)
	if err != nil {
		return nil, "", err
	}

	parser := NewStructuredReviewParser(s.cfg.Logger)
	chain, err := chains.NewLLMChain(
		s.cfg.GeneratorLLM,
		prompts.NewPromptTemplate(promptStr),
		chains.WithOutputParser(parser),
	)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create LLM chain: %w", err)
	}

	structuredReview, err := chain.Call(ctx, nil)
	if err != nil {
		return nil, "", err
	}

	if structuredReview.Verdict == "" {
		structuredReview.Verdict = core.VerdictComment // Default if missing
	}

	// Add disclaimer to summary if context was empty
	if contextEmpty {
		structuredReview.Summary = "**Note:** This review was generated without repository context. Verify findings against actual codebase.\n\n" + structuredReview.Summary
	}

	return structuredReview, parser.Raw, nil
}
