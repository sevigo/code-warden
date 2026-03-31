package review

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/goframe/chains"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// consensusMapFunc creates a map function for the MapReduceChain that generates a review with a specific model.
func (s *Service) consensusMapFunc(event *core.GitHubEvent, promptData map[string]string, resultsTracker *[]ComparisonResult, mu *sync.Mutex, dir, ts string) func(ctx context.Context, modelName string) (ComparisonResult, error) {
	return func(ctx context.Context, modelName string) (ComparisonResult, error) {
		modelStart := time.Now()
		llmModel, err := s.cfg.GetLLM(ctx, modelName)
		if err != nil {
			s.cfg.Logger.Warn("failed to get model for consensus", "model", modelName, "error", err)
			return ComparisonResult{Model: modelName, Error: err}, nil
		}
		prompt, err := s.cfg.PromptMgr.Render(llm.CodeReviewPrompt, promptData)
		if err != nil {
			s.cfg.Logger.Warn("failed to render prompt for model", "model", modelName, "error", err)
			return ComparisonResult{Model: modelName, Error: err}, nil
		}
		timeout := s.getConsensusTimeout()
		tCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		resp, err := llmModel.Call(tCtx, prompt)
		modelTime := time.Since(modelStart)

		result := ComparisonResult{Model: modelName, Review: resp, Duration: modelTime, Error: err}
		mu.Lock()
		*resultsTracker = append(*resultsTracker, result)
		mu.Unlock()

		// Save artifact immediately so we don't miss late arrivals
		if err == nil && strings.TrimSpace(resp) != "" {
			SaveReviewArtifact(s.cfg.Logger, dir, result, event, ts)
		}

		if err != nil {
			s.cfg.Logger.Warn("model review failed",
				"model", modelName,
				"error", err,
				"duration", modelTime.String())
		} else {
			s.cfg.Logger.Info("model review completed",
				"model", modelName,
				"review_len", len(resp),
				"duration", modelTime.String())
		}
		return result, nil
	}
}

func (s *Service) consensusReduceFunc(repoConfig *core.RepoConfig, event *core.GitHubEvent, contextString string, changedFiles []internalgithub.ChangedFile, contextBuildTime time.Duration, reviewsDir string) func(ctx context.Context, results []ComparisonResult) (string, error) {
	return func(ctx context.Context, results []ComparisonResult) (string, error) {
		s.cfg.Logger.Info("quorum reached, starting consensus synthesis",
			"models_participating", len(results),
			"models", getSuccessfulModels(results))
		synthStart := time.Now()
		rawConsensus, validReviews, err := s.synthesizeConsensus(ctx, repoConfig, event, results, contextString, changedFiles, contextBuildTime, reviewsDir)
		synthTime := time.Since(synthStart)

		if err != nil {
			// Graceful degradation: if synthesis fails, use the best available review
			s.cfg.Logger.Warn("consensus synthesis failed, falling back to best single review",
				"error", err,
				"synthesis_time", synthTime.String())

			fallbackReview, fallbackModel := s.selectBestReview(results)
			if fallbackReview != "" {
				s.cfg.Logger.Info("using fallback review", "model", fallbackModel, "review_len", len(fallbackReview))
				fallbackDisclaimer := fmt.Sprintf("\n\n> ⚠️ **Fallback Mode**\n> Consensus synthesis failed. Using review from: %s.\n> *Mistakes are possible. Please verify critical issues.*", fallbackModel)
				return fallbackReview + fallbackDisclaimer, nil
			}
			return "", fmt.Errorf("consensus synthesis failed and no valid reviews available: %w", err)
		}

		s.cfg.Logger.Info("consensus synthesis completed",
			"valid_reviews", len(validReviews),
			"synthesis_time", synthTime.String())

		return rawConsensus, nil
	}
}

// GenerateConsensusReview generates a consensus review from multiple LLM models.
//
//nolint:funlen // Complex consensus workflow requiring multiple stages
func (s *Service) GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, models []string, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	startTime := time.Now()
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}
	if err := s.validateConsensusParams(repo, event, models); err != nil {
		return nil, "", err
	}

	if len(models) < 1 {
		return nil, "", fmt.Errorf("need at least 1 comparison model, got %d", len(models))
	}

	// Use context builder with impact tracking for profile calculation
	contextResult := s.cfg.BuildContextWithImpact(ctx, repo.QdrantCollectionName, s.cfg.EmbedderModel, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)
	contextString := contextResult.FullContext
	definitionsContext := contextResult.DefinitionsContext
	impactRadius := contextResult.ImpactRadius

	// Detect duplications by generating embeddings for the exact added lines
	if dupCtx := s.checkCodeDuplication(ctx, repo.QdrantCollectionName, changedFiles); dupCtx != "" {
		contextString += "\n\n" + dupCtx
	}

	contextBuildTime := time.Since(startTime)

	s.cfg.Logger.Info("stage started", "name", "ConsensusGathering", "models_count", len(models),
		"context_build_time", contextBuildTime.String())
	s.cfg.Logger.Debug("consensus context gathered",
		"context_len", len(contextString),
		"definitions_len", len(definitionsContext),
		"impact_radius", impactRadius,
	)

	// Warn if no context was retrieved
	contextWasEmpty := contextIsEmpty(contextString, definitionsContext)
	if contextWasEmpty {
		s.cfg.Logger.Warn("HIGH HALLUCINATION RISK: no context retrieved from vector store - consensus review will be based solely on diff",
			"repo", event.RepoFullName,
			"pr", event.PRNumber,
			"changed_files", len(changedFiles),
		)
		contextString = "**WARNING: No repository context available. Reviews based solely on diff without repository context. Verify findings against actual codebase.**"
		definitionsContext = "**WARNING: No type definitions resolved.**"
	}

	// Calculate review profile for consensus
	linesAdded, linesDeleted := calculateLinesChanged(changedFiles)
	changedFilePaths := extractFilenames(changedFiles)
	testCoverage := core.HasTestCoverage(changedFilePaths)
	docsOnly := core.IsDocsOnly(changedFilePaths)
	complexity := core.CalculateProfile(linesAdded, linesDeleted, len(changedFiles), impactRadius, testCoverage, docsOnly, changedFilePaths)

	s.cfg.Logger.Info("consensus review profile calculated",
		"profile", complexity.Profile,
		"score", complexity.Score,
		"impact_radius", complexity.ImpactRadius,
		"high_impact", complexity.HighImpact,
		"high_risk", complexity.HighRisk,
	)

	// Prepare for artifact saving
	timestamp := time.Now().Format("20060102_150405_000000000")
	reviewsDir := filepath.Join(filepath.Dir(repo.ClonePath), "reviews")
	if err := EnsureReviewsDir(s.cfg.Logger, reviewsDir); err != nil {
		s.cfg.Logger.Warn("failed to ensure reviews directory, artifacts might not be saved", "error", err)
	}

	// Render profile instruction for consensus
	profileInstruction, err := s.cfg.PromptMgr.Render("review_profile", complexity)
	if err != nil {
		s.cfg.Logger.Warn("failed to render review profile for consensus, using default", "error", err)
		profileInstruction = ""
	}

	promptData := s.buildReviewPromptDataWithProfile(event, repoConfig, contextString, definitionsContext, diff, changedFiles, profileInstruction)

	// Track model results for fallback
	var modelResults []ComparisonResult
	var modelResultsMu sync.Mutex

	chain := chains.NewMapReduceChain(
		s.consensusMapFunc(event, promptData, &modelResults, &modelResultsMu, reviewsDir, timestamp),
		s.consensusReduceFunc(repoConfig, event, contextString, changedFiles, contextBuildTime, reviewsDir),
		chains.WithMaxConcurrency[string, ComparisonResult, string](2),
		chains.WithQuorum[string, ComparisonResult, string](s.cfg.ConsensusQuorum),
	)

	rawConsensus, err := chain.Call(ctx, models)
	if err != nil {
		return nil, "", fmt.Errorf("failed to gather consensus reviews: %w", err)
	}

	parser := NewStructuredReviewParser(s.cfg.Logger)
	structuredReview, err := parser.Parse(ctx, rawConsensus)
	if err != nil {
		s.cfg.Logger.Error("FATAL: failed to parse consensus review - final report structure is broken. Check LLM output for tagging errors.", "error", err, "pr", event.PRNumber)
		structuredReview = &core.StructuredReview{Summary: rawConsensus}
	} else {
		if err := s.validateStructuredReview(ctx, event, structuredReview); err != nil {
			return nil, "", err
		}
		// Add disclaimer to summary if context was empty (mirroring GenerateReview)
		if contextWasEmpty {
			structuredReview.Summary = "**Note:** This consensus review was generated without repository context. Verify findings against actual codebase.\n\n" + structuredReview.Summary
		}
	}

	successfulModels := getSuccessfulModels(modelResults)
	totalTime := time.Since(startTime)
	s.cfg.Logger.Info("consensus review completed",
		"total_time", totalTime.String(),
		"models_requested", len(models),
		"models_eventually_succeeded", len(successfulModels),
		"total_completed_tasks", len(modelResults),
	)

	// Construct final disclaimer with timings and model info
	// Synthesis time is approximated as total time minus context build time
	synthesisTime := totalTime - contextBuildTime
	if synthesisTime < 0 {
		synthesisTime = 0
	}
	modelsList := strings.Join(successfulModels, ", ")
	disclaimer := fmt.Sprintf("\n\n---\n> 🤖 **AI Consensus Review**\n> **Models:** %s\n> **Context:** %s | **Synthesis:** %s | **Total:** %s\n> *Mistakes are possible. Please verify critical issues.*",
		modelsList,
		contextBuildTime.Truncate(time.Millisecond),
		synthesisTime.Truncate(time.Millisecond),
		totalTime.Truncate(time.Millisecond),
	)

	// Update summary and raw output
	structuredReview.Summary += disclaimer
	rawConsensus += disclaimer

	// Add profile metadata to consensus result
	structuredReview.ReviewProfile = string(complexity.Profile)
	structuredReview.ComplexityScore = complexity.Score
	structuredReview.ImpactRadius = complexity.ImpactRadius

	return structuredReview, rawConsensus, nil
}

// selectBestReview selects the longest valid review from the results as a fallback.
func (s *Service) selectBestReview(results []ComparisonResult) (string, string) {
	var bestReview string
	var bestModel string
	for _, res := range results {
		if res.Error == nil && len(res.Review) > len(bestReview) {
			bestReview = res.Review
			bestModel = res.Model
		}
	}
	return bestReview, bestModel
}

func getSuccessfulModels(results []ComparisonResult) []string {
	var models []string
	for _, res := range results {
		if res.Error == nil && strings.TrimSpace(res.Review) != "" {
			models = append(models, res.Model)
		}
	}
	return models
}

func (s *Service) validateStructuredReview(ctx context.Context, event *core.GitHubEvent, review *core.StructuredReview) error {
	// check review integrity
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if review.Verdict == "" {
		s.cfg.Logger.Warn("consensus review generated without a verdict", "pr", event.PRNumber)
		review.Verdict = core.VerdictComment
	}
	if review.Summary == "" {
		s.cfg.Logger.Warn("consensus review generated without a summary", "pr", event.PRNumber)
	}
	if review.Verdict == core.VerdictRequestChanges && len(review.Suggestions) == 0 {
		s.cfg.Logger.Error("CONSENSUS INCONSISTENCY: verdict is REQUEST_CHANGES but no suggestions were captured", "pr", event.PRNumber)
	}
	return nil
}

func (s *Service) validateConsensusParams(repo *storage.Repository, event *core.GitHubEvent, models []string) error {
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

func (s *Service) synthesizeConsensus(ctx context.Context, repoConfig *core.RepoConfig, event *core.GitHubEvent, results []ComparisonResult, context string, changedFiles []internalgithub.ChangedFile, contextBuildTime time.Duration, reviewsDir string) (string, []string, error) {
	var validReviews []string
	var reviewsBuilder strings.Builder
	timestampStart := time.Now()

	// Use original naming to match other artifacts (this needs to be passed in for perfect grouping)
	// For now, generate a new one or passed from top - let's keep it simple for now
	timestamp := timestampStart.Format("20060102_150405_000000000")

	sort.Slice(results, func(i, j int) bool {
		return results[i].Model < results[j].Model
	})

	for _, res := range results {
		if res.Error != nil || strings.TrimSpace(res.Review) == "" {
			continue
		}
		fmt.Fprintf(&reviewsBuilder, "\n--- Review from %s ---\n", res.Model)
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
		"ChangedFiles":       formatChangedFiles(changedFiles),
		"CustomInstructions": strings.Join(repoConfig.CustomInstructions, "\n"),
	}

	rawConsensus, err := s.generateResponseWithPrompt(ctx, event, llm.ConsensusReviewPrompt, promptData)
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate consensus: %w", err)
	}

	totalSynthesisTime := time.Since(timestampStart)
	s.cfg.Logger.Debug("consensus synthesis complete", "valid_reviews", len(validReviews), "duration", totalSynthesisTime.String())

	SaveConsensusArtifact(s.cfg.Logger, reviewsDir, rawConsensus, timestamp, event, totalSynthesisTime, validReviews, contextBuildTime)
	return rawConsensus, validReviews, nil
}
