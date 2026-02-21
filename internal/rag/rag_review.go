package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sevigo/goframe/llms"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

type ComparisonResult struct {
	Model  string
	Review string
	Error  error
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

	contextString, definitionsContext := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

	promptData := map[string]string{
		"Title":              event.PRTitle,
		"Description":        event.PRBody,
		"Language":           event.Language,
		"CustomInstructions": strings.Join(repoConfig.CustomInstructions, "\n"),
		"ChangedFiles":       r.formatChangedFiles(changedFiles),
		"Context":            contextString,
		"Definitions":        definitionsContext,
		"Diff":               diff,
	}

	rawReview, err := r.generateResponseWithPrompt(ctx, event, llm.CodeReviewPrompt, promptData)
	if err != nil {
		return nil, err.Error(), err
	}

	// Parse Markdown Review
	structuredReview, err := llm.ParseMarkdownReview(ctx, rawReview, r.logger)
	if err != nil {
		r.logger.Warn("failed to parse markdown review, using raw output as fallback", "error", err)
		// Fallback: Use raw output as summary
		structuredReview = &core.StructuredReview{
			Summary: rawReview,
		}
	}

	if structuredReview.Verdict == "" {
		structuredReview.Verdict = core.VerdictComment // Default if missing
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
	definitionsContext := ""
	if contextString == "" {
		contextString, definitionsContext = r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)
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
		"Definitions":        definitionsContext,
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

		prompt, err := r.promptMgr.Render(llm.CodeReviewPrompt, localPromptData)
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
	contextString, _ := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

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
	structuredReview, err := llm.ParseMarkdownReview(ctx, rawConsensus, r.logger)
	if err != nil {
		r.logger.Error("FATAL: failed to parse consensus review - final report structure is broken. Check LLM output for tagging errors.", "error", err, "pr", event.PRNumber)
		structuredReview = &core.StructuredReview{Summary: rawConsensus}
	} else if err := r.validateStructuredReview(ctx, event, structuredReview); err != nil {
		return nil, "", err
	}

	disclaimer := fmt.Sprintf("\n\n> 🤖 **AI Consensus Review**\n> Generated by synthesizing findings from: %s. \n> *Mistakes are possible. Please verify critical issues.*", strings.Join(validReviews, ", "))
	structuredReview.Summary += disclaimer

	return structuredReview, rawConsensus, nil
}

func (r *ragService) validateStructuredReview(ctx context.Context, event *core.GitHubEvent, review *core.StructuredReview) error {
	// Verify integrity of consensus review
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

	rawConsensus, err := r.generateResponseWithPrompt(ctx, event, llm.ConsensusReviewPrompt, promptData)
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

// formatChangedFiles returns a markdown-formatted list of changed file paths
// to include in the LLM prompt.
func (r *ragService) formatChangedFiles(files []internalgithub.ChangedFile) string {
	var builder strings.Builder
	for _, file := range files {
		builder.WriteString(fmt.Sprintf("- `%s`\n", file.Filename))
	}
	return builder.String()
}
