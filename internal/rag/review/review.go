package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/prompts"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/rag/metadata"
	"github.com/sevigo/code-warden/internal/storage"
)

const (
	// Minimum consecutive added lines to consider a meaningful chunk for duplication detection
	minChunkLines = 4

	// Maximum chunks to check for duplication (limits vector DB queries on large PRs)
	maxChunksToCheck = 10

	// Minimum cosine similarity to consider a match as potential duplicate
	duplicationSimilarityThreshold = 0.85
)

// buildPRDescription builds the PR description string passed to BuildContext,
// including the PR title, body, and commit messages (first line each).
func buildPRDescription(event *core.GitHubEvent) string {
	desc := event.PRTitle + "\n" + event.PRBody
	if len(event.CommitMessages) == 0 {
		return desc
	}
	var sb strings.Builder
	sb.WriteString(desc)
	sb.WriteString("\n\n## Commit Messages\n")
	for _, msg := range event.CommitMessages {
		firstLine := msg
		if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
			firstLine = msg[:idx]
		}
		fmt.Fprintf(&sb, "- %s\n", strings.TrimSpace(firstLine))
	}
	return sb.String()
}

// extractAddedChunks parses a git patch and returns blocks of consecutively added lines.
func extractAddedChunks(patch string) []string {
	var chunks []string
	var currentChunk []string

	lines := strings.Split(patch, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			content := strings.TrimPrefix(line, "+")
			// Skip entirely empty lines or simple brackets to avoid noisy chunks
			trimmed := strings.TrimSpace(content)
			if trimmed == "" || trimmed == "}" || trimmed == "{" || trimmed == "};" || trimmed == "()" {
				continue
			}
			currentChunk = append(currentChunk, content)
		} else {
			if len(currentChunk) >= minChunkLines {
				chunks = append(chunks, strings.Join(currentChunk, "\n"))
			}
			currentChunk = nil
		}
	}
	if len(currentChunk) >= minChunkLines {
		chunks = append(chunks, strings.Join(currentChunk, "\n"))
	}
	return chunks
}

// checkCodeDuplication queries the VectorDB for semantic duplicates of the newly added code chunks.
func (s *Service) checkCodeDuplication(ctx context.Context, collectionName string, changedFiles []internalgithub.ChangedFile) string {
	if s.cfg.VectorStore == nil {
		return ""
	}

	var allChunks []string
	for _, cf := range changedFiles {
		if cf.Patch == "" {
			continue
		}
		chunks := extractAddedChunks(cf.Patch)
		allChunks = append(allChunks, chunks...)
	}

	if len(allChunks) == 0 {
		return ""
	}

	// Limit chunks to avoid blowing up the vector DB with thousands of queries on massive PRs.
	if len(allChunks) > maxChunksToCheck {
		allChunks = allChunks[:maxChunksToCheck]
	}

	scopedStore := s.cfg.VectorStore.ForRepo(collectionName, s.cfg.EmbedderModel)

	var duplicates strings.Builder
	foundCount := 0

	for _, chunk := range allChunks {
		// Query multiple results to find all potential duplicates, not just the closest
		results, err := scopedStore.SimilaritySearchWithScores(ctx, chunk, 5)
		if err != nil || len(results) == 0 {
			continue
		}

		for _, match := range results {
			if match.Score > duplicationSimilarityThreshold {
				source, _ := match.Document.Metadata["source"].(string)
				line := metadata.ExtractLineNumber(match.Document.Metadata)

				fmt.Fprintf(&duplicates, "### Potential Duplicate %d (Similarity Score: %.2f)\n", foundCount+1, match.Score)
				fmt.Fprintf(&duplicates, "**Newly Added Code:**\n```\n%s\n```\n", chunk)
				fmt.Fprintf(&duplicates, "**Existing Code Found in `%s` (Line %d):**\n```\n%s\n```\n\n", source, line, match.Document.PageContent)
				foundCount++
			}
		}
	}

	if foundCount == 0 {
		return ""
	}

	return "POTENTIAL CODE DUPLICATIONS FOUND:\n" +
		"The following existing functions semantically match newly added code in this PR. " +
		"Analyze these matches. If the new code duplicates existing functionality, suggest " +
		"replacing the new code with a call to the existing function (DRY principle).\n\n" +
		duplicates.String()
}

// calculateLinesChanged counts added and deleted lines from changed files.
func calculateLinesChanged(changedFiles []internalgithub.ChangedFile) (added, deleted int) {
	for _, cf := range changedFiles {
		if cf.Patch == "" {
			continue
		}
		lines := strings.Split(cf.Patch, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				added++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				deleted++
			}
		}
	}
	return added, deleted
}

// GenerateReview generates a structured code review using the RAG pipeline.
//
//nolint:funlen // Complex function that orchestrates the review pipeline
func (s *Service) GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}

	s.cfg.Logger.Info("preparing data for a full review", "repo", event.RepoFullName, "pr", event.PRNumber, "embedder", s.cfg.EmbedderModel)
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

	// Use context builder with impact tracking
	var contextString, definitionsContext string
	var impactRadius int

	if s.cfg.BuildContextWithImpact != nil {
		contextResult := s.cfg.BuildContextWithImpact(ctx, repo.QdrantCollectionName, s.cfg.EmbedderModel, repo.ClonePath, changedFiles, buildPRDescription(event))
		contextString = contextResult.FullContext
		definitionsContext = contextResult.DefinitionsContext
		impactRadius = contextResult.ImpactRadius
	} else {
		// Fallback to old method
		contextString, definitionsContext = s.cfg.BuildContext(ctx, repo.QdrantCollectionName, s.cfg.EmbedderModel, repo.ClonePath, changedFiles, buildPRDescription(event))
	}

	// Phase 2: LLM-directed gap filling (only when Phase 1 returned meaningful context)
	if s.cfg.Investigate != nil && !contextIsEmpty(contextString, definitionsContext) {
		additionalContext := s.cfg.Investigate(ctx, repo.QdrantCollectionName, diff, contextString, definitionsContext)
		if additionalContext != "" {
			contextString += "\n\n" + additionalContext
		}
	}

	// Detect duplications by generating embeddings for the exact added lines
	duplicationContext := s.checkCodeDuplication(ctx, repo.QdrantCollectionName, changedFiles)
	if duplicationContext != "" {
		contextString = contextString + "\n\n" + duplicationContext
	}

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

	// Calculate review profile
	linesAdded, linesDeleted := calculateLinesChanged(changedFiles)
	testCoverage := core.HasTestCoverage(extractFilenames(changedFiles))
	docsOnly := core.IsDocsOnly(extractFilenames(changedFiles))
	complexity := core.CalculateProfile(linesAdded, linesDeleted, len(changedFiles), impactRadius, testCoverage, docsOnly)

	s.cfg.Logger.Info("review profile calculated",
		"profile", complexity.Profile,
		"score", complexity.Score,
		"impact_radius", complexity.ImpactRadius,
		"high_impact", complexity.HighImpact,
		"lines_added", linesAdded,
		"lines_deleted", linesDeleted,
		"files_changed", len(changedFiles),
	)

	// Render profile instruction
	profileInstruction, err := s.cfg.PromptMgr.Render("review_profile", complexity)
	if err != nil {
		s.cfg.Logger.Warn("failed to render review profile, using default", "error", err)
		profileInstruction = "" // Will use default thorough profile
	}

	promptData := s.buildReviewPromptDataWithProfile(event, repoConfig, contextString, definitionsContext, diff, changedFiles, profileInstruction)

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

	// Filter and validate suggestions with profile-specific threshold
	validator := NewSuggestionValidator(diff, changedFiles)
	filter := NewFilterForProfile(complexity.Profile)
	structuredReview = filter.FilterAndRank(structuredReview, validator, s.cfg.Logger.Info)

	// Add complexity score to result for UI display
	structuredReview.ReviewProfile = string(complexity.Profile)
	structuredReview.ComplexityScore = complexity.Score
	structuredReview.ImpactRadius = complexity.ImpactRadius

	// Add disclaimer to summary if context was empty
	if contextEmpty {
		structuredReview.Summary = "**Note:** This review was generated without repository context. Verify findings against actual codebase.\n\n" + structuredReview.Summary
	}

	return structuredReview, parser.Raw, nil
}

// extractFilenames returns just the filenames from changed files.
func extractFilenames(changedFiles []internalgithub.ChangedFile) []string {
	filenames := make([]string, len(changedFiles))
	for i, cf := range changedFiles {
		filenames[i] = cf.Filename
	}
	return filenames
}
