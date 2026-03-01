package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/output"
	"github.com/sevigo/goframe/prompts"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

type ComparisonResult struct {
	Model    string
	Review   string
	Duration time.Duration
	Error    error
}

type structuredReviewParser struct {
	logger *slog.Logger
	raw    string
}

func (p *structuredReviewParser) Parse(ctx context.Context, outputStr string) (*core.StructuredReview, error) {
	p.raw = outputStr
	xmlParser := output.NewXMLParser[*core.StructuredReview]("review")
	parsed, err := xmlParser.Parse(ctx, outputStr)
	if err != nil {
		p.logger.Warn("failed to parse XML review, trying manual tag extraction", "error", err)
		return llm.ParseLegacyMarkdownReview(outputStr)
	}
	return parsed, nil
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

	// If changedFiles is empty (internal review), extract them from the diff
	if len(changedFiles) == 0 {
		changedFiles = ParseDiff(diff)
		r.logger.Info("extracted changed files from diff for internal review", "count", len(changedFiles))
	}

	contextString, definitionsContext := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

	// Check for empty context to warn about hallucination risk
	contextIsEmpty := contextIsEmpty(contextString, definitionsContext)
	if contextIsEmpty {
		r.logger.Warn("HIGH HALLUCINATION RISK: no context retrieved from vector store - review will be based solely on diff without repository context",
			"repo", event.RepoFullName,
			"pr", event.PRNumber,
			"changed_files", len(changedFiles),
		)
		// Inject warning messages into context for the LLM
		contextString = "**WARNING: No repository context available. Review based solely on the provided diff. Do not assume external code structure.**"
		definitionsContext = "**WARNING: No type definitions resolved. Verify types are defined outside this diff.**"
	}

	promptData := r.buildReviewPromptData(event, repoConfig, contextString, definitionsContext, diff, changedFiles)

	promptStr, err := r.promptMgr.Render(llm.CodeReviewPrompt, promptData)
	if err != nil {
		return nil, "", err
	}

	parser := &structuredReviewParser{logger: r.logger}
	chain, err := chains.NewLLMChain(
		r.generatorLLM,
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
	if contextIsEmpty {
		structuredReview.Summary = "**Note:** This review was generated without repository context. Verify findings against actual codebase.\n\n" + structuredReview.Summary
	}

	return structuredReview, parser.raw, nil
}

// ParseDiff parses a unified git diff into a slice of ChangedFile.
func ParseDiff(diff string) []internalgithub.ChangedFile {
	var files []internalgithub.ChangedFile
	var currentFile *internalgithub.ChangedFile

	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// Start of a new file
			if currentFile != nil {
				files = append(files, *currentFile)
			}
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				// Format: diff --git a/path/to/file b/path/to/file
				// We want the path after b/
				filename := strings.TrimPrefix(parts[3], "b/")
				currentFile = &internalgithub.ChangedFile{
					Filename: filename,
				}
			}
		case strings.HasPrefix(line, "@@"):
			// Hunk header — skip, not part of the patch body
			continue
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			// Diff file headers — skip, not part of the patch body
			continue
		case currentFile != nil:
			// Append line to current file patch
			currentFile.Patch += line + "\n"
		}
	}

	if currentFile != nil {
		files = append(files, *currentFile)
	}

	return files
}

func (r *ragService) buildReviewPromptData(event *core.GitHubEvent, repoConfig *core.RepoConfig, contextString, definitionsContext, diff string, changedFiles []internalgithub.ChangedFile) map[string]string {
	return map[string]string{
		"Title":              event.PRTitle,
		"Description":        event.PRBody,
		"Language":           event.Language,
		"CustomInstructions": strings.Join(repoConfig.CustomInstructions, "\n"),
		"ChangedFiles":       r.formatChangedFiles(changedFiles),
		"Context":            contextString,
		"Definitions":        definitionsContext,
		"Diff":               diff,
	}
}

// contextIsEmpty checks if both context strings are empty.
// This helps detect high hallucination risk.
func contextIsEmpty(contextString, definitionsContext string) bool {
	return contextString == "" && definitionsContext == ""
}

// consensusMapFunc creates a map function for the MapReduceChain that generates a review with a specific model.
func (r *ragService) consensusMapFunc(event *core.GitHubEvent, promptData map[string]string, resultsTracker *[]ComparisonResult, mu *sync.Mutex, dir, ts string) func(ctx context.Context, modelName string) (ComparisonResult, error) {
	return func(ctx context.Context, modelName string) (ComparisonResult, error) {
		modelStart := time.Now()
		llmModel, err := r.getOrCreateLLM(ctx, modelName)
		if err != nil {
			r.logger.Warn("failed to get model for consensus", "model", modelName, "error", err)
			return ComparisonResult{Model: modelName, Error: err}, nil
		}
		prompt, err := r.promptMgr.Render(llm.CodeReviewPrompt, promptData)
		if err != nil {
			r.logger.Warn("failed to render prompt for model", "model", modelName, "error", err)
			return ComparisonResult{Model: modelName, Error: err}, nil
		}
		timeout := r.getConsensusTimeout()
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
			r.saveReviewArtifact(dir, result, event, ts)
		}

		if err != nil {
			r.logger.Warn("model review failed",
				"model", modelName,
				"error", err,
				"duration", modelTime.String())
		} else {
			r.logger.Info("model review completed",
				"model", modelName,
				"review_len", len(resp),
				"duration", modelTime.String())
		}
		return result, nil
	}
}

func (r *ragService) consensusReduceFunc(repoConfig *core.RepoConfig, event *core.GitHubEvent, contextString string, changedFiles []internalgithub.ChangedFile, contextBuildTime time.Duration) func(ctx context.Context, results []ComparisonResult) (string, error) {
	return func(ctx context.Context, results []ComparisonResult) (string, error) {
		r.logger.Info("quorum reached, starting consensus synthesis",
			"models_participating", len(results),
			"models", getSuccessfulModels(results))
		synthStart := time.Now()
		rawConsensus, validReviews, err := r.synthesizeConsensus(ctx, repoConfig, event, results, contextString, changedFiles, contextBuildTime)
		synthTime := time.Since(synthStart)

		if err != nil {
			// Graceful degradation: if synthesis fails, use the best available review
			r.logger.Warn("consensus synthesis failed, falling back to best single review",
				"error", err,
				"synthesis_time", synthTime.String())

			fallbackReview, fallbackModel := r.selectBestReview(results)
			if fallbackReview != "" {
				r.logger.Info("using fallback review", "model", fallbackModel, "review_len", len(fallbackReview))
				fallbackDisclaimer := fmt.Sprintf("\n\n> ⚠️ **Fallback Mode**\n> Consensus synthesis failed. Using review from: %s.\n> *Mistakes are possible. Please verify critical issues.*", fallbackModel)
				return fallbackReview + fallbackDisclaimer, nil
			}
			return "", fmt.Errorf("consensus synthesis failed and no valid reviews available: %w", err)
		}

		r.logger.Info("consensus synthesis completed",
			"valid_reviews", len(validReviews),
			"synthesis_time", synthTime.String())

		return rawConsensus, nil
	}
}

func (r *ragService) GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, models []string, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	startTime := time.Now()
	if repoConfig == nil {
		repoConfig = core.DefaultRepoConfig()
	}
	if err := r.validateConsensusParams(repo, event, models); err != nil {
		return nil, "", err
	}

	if len(models) < 1 {
		return nil, "", fmt.Errorf("need at least 1 comparison model, got %d", len(models))
	}

	contextString, definitionsContext := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)
	contextBuildTime := time.Since(startTime)

	r.logger.Info("stage started", "name", "ConsensusGathering", "models_count", len(models),
		"context_build_time", contextBuildTime.String())
	r.logger.Debug("consensus context gathered",
		"context_len", len(contextString),
		"definitions_len", len(definitionsContext),
	)

	// Warn if no context was retrieved
	contextWasEmpty := contextIsEmpty(contextString, definitionsContext)
	if contextWasEmpty {
		r.logger.Warn("HIGH HALLUCINATION RISK: no context retrieved from vector store - consensus review will be based solely on diff",
			"repo", event.RepoFullName,
			"pr", event.PRNumber,
			"changed_files", len(changedFiles),
		)
		contextString = "**WARNING: No repository context available. Reviews based solely on diff without repository context. Verify findings against actual codebase.**"
		definitionsContext = "**WARNING: No type definitions resolved.**"
	}

	// Prepare for artifact saving
	timestamp := time.Now().Format("20060102_150405_000000000")
	reviewsDir := "reviews"
	if err := r.ensureReviewsDir(reviewsDir); err != nil {
		r.logger.Warn("failed to ensure reviews directory, artifacts might not be saved", "error", err)
	}

	promptData := r.buildReviewPromptData(event, repoConfig, contextString, definitionsContext, diff, changedFiles)

	// Track model results for fallback
	var modelResults []ComparisonResult
	var modelResultsMu sync.Mutex

	chain := chains.NewMapReduceChain(
		r.consensusMapFunc(event, promptData, &modelResults, &modelResultsMu, reviewsDir, timestamp),
		r.consensusReduceFunc(repoConfig, event, contextString, changedFiles, contextBuildTime),
		chains.WithMaxConcurrency[string, ComparisonResult, string](2),
		chains.WithQuorum[string, ComparisonResult, string](r.cfg.AI.ConsensusQuorum),
	)

	rawConsensus, err := chain.Call(ctx, models)
	if err != nil {
		return nil, "", fmt.Errorf("failed to gather consensus reviews: %w", err)
	}

	parser := &structuredReviewParser{logger: r.logger}
	structuredReview, err := parser.Parse(ctx, rawConsensus)
	if err != nil {
		r.logger.Error("FATAL: failed to parse consensus review - final report structure is broken. Check LLM output for tagging errors.", "error", err, "pr", event.PRNumber)
		structuredReview = &core.StructuredReview{Summary: rawConsensus}
	} else {
		if err := r.validateStructuredReview(ctx, event, structuredReview); err != nil {
			return nil, "", err
		}
		// Add disclaimer to summary if context was empty (mirroring GenerateReview)
		if contextWasEmpty {
			structuredReview.Summary = "**Note:** This consensus review was generated without repository context. Verify findings against actual codebase.\n\n" + structuredReview.Summary
		}
	}

	successfulModels := getSuccessfulModels(modelResults)
	totalTime := time.Since(startTime)
	r.logger.Info("consensus review completed",
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

	return structuredReview, rawConsensus, nil
}

// selectBestReview selects the longest valid review from the results as a fallback.
func (r *ragService) selectBestReview(results []ComparisonResult) (string, string) {
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

func (r *ragService) validateStructuredReview(ctx context.Context, event *core.GitHubEvent, review *core.StructuredReview) error {
	// check review integrity
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if review.Verdict == "" {
		r.logger.Warn("consensus review generated without a verdict", "pr", event.PRNumber)
		review.Verdict = core.VerdictComment
	}
	if review.Summary == "" {
		r.logger.Warn("consensus review generated without a summary", "pr", event.PRNumber)
	}
	if review.Verdict == core.VerdictRequestChanges && len(review.Suggestions) == 0 {
		r.logger.Error("CONSENSUS INCONSISTENCY: verdict is REQUEST_CHANGES but no suggestions were captured", "pr", event.PRNumber)
	}
	return nil
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

func (r *ragService) synthesizeConsensus(ctx context.Context, repoConfig *core.RepoConfig, event *core.GitHubEvent, results []ComparisonResult, context string, changedFiles []internalgithub.ChangedFile, contextBuildTime time.Duration) (string, []string, error) {
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

	rawConsensus, err := r.generateResponseWithPrompt(ctx, event, llm.ConsensusReviewPrompt, promptData)
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate consensus: %w", err)
	}

	totalSynthesisTime := time.Since(timestampStart)
	r.logger.Debug("consensus synthesis complete", "valid_reviews", len(validReviews), "duration", totalSynthesisTime.String())

	reviewsDir := "reviews"
	go r.saveConsensusArtifact(reviewsDir, rawConsensus, timestamp, event, totalSynthesisTime, validReviews, contextBuildTime)
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
	header := fmt.Sprintf("# Code Review by %s\n\n**Date:** %s\n**PR:** %s/%s #%d\n**Duration:** %s\n\n",
		res.Model,
		time.Now().Format(time.RFC3339),
		event.RepoOwner,
		event.RepoName,
		event.PRNumber,
		res.Duration.String(),
	)
	if err := os.WriteFile(filename, []byte(header+res.Review), 0600); err != nil {
		r.logger.Warn("failed to save review artifact", "model", res.Model, "error", err)
	}
}

func (r *ragService) saveConsensusArtifact(dir, raw, ts string, event *core.GitHubEvent, duration time.Duration, models []string, contextDuration time.Duration) {
	filename := filepath.Join(dir, fmt.Sprintf("review_consensus_%s.md", ts))
	header := fmt.Sprintf("# AI Consensus Review\n\n**Date:** %s\n**PR:** %s/%s #%d\n**Context Build Duration:** %s\n**Synthesis Duration:** %s\n**Contributing Models:** %s\n\n",
		time.Now().Format(time.RFC3339),
		event.RepoOwner,
		event.RepoName,
		event.PRNumber,
		contextDuration.String(),
		duration.String(),
		strings.Join(models, ", "),
	)
	if err := os.WriteFile(filename, []byte(header+raw), 0600); err != nil {
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

	// Add a short hash to prevent name collisions
	h := sha256.New()
	h.Write([]byte(modelName))
	hashStr := hex.EncodeToString(h.Sum(nil))[:16]

	// Handle Windows reserved names
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

// formatChangedFiles returns a markdown-formatted list of changed file paths.
func (r *ragService) formatChangedFiles(files []internalgithub.ChangedFile) string {
	var builder strings.Builder
	for _, file := range files {
		builder.WriteString(fmt.Sprintf("- `%s`\n", file.Filename))
	}
	return builder.String()
}

// getConsensusTimeout returns the timeout for individual model reviews in consensus mode.
// Falls back to 5 minutes if not configured or invalid.
func (r *ragService) getConsensusTimeout() time.Duration {
	const defaultTimeout = 5 * time.Minute
	if r.cfg.AI.ConsensusTimeout == "" {
		return defaultTimeout
	}
	d, err := time.ParseDuration(r.cfg.AI.ConsensusTimeout)
	if err != nil {
		r.logger.Warn("invalid consensus_timeout config, using default", "error", err, "default", defaultTimeout)
		return defaultTimeout
	}
	return d
}
