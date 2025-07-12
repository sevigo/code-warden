// Package jobs defines background tasks such as code reviews.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/llm"
)

// ReviewJob is a background job that performs AI-assisted code reviews.
type ReviewJob struct {
	cfg        *config.Config
	ragService llm.RAGService
	logger     *slog.Logger
}

// NewReviewJob creates a new ReviewJob with config, RAG service, and logger.
func NewReviewJob(cfg *config.Config, rag llm.RAGService, logger *slog.Logger) core.Job {
	if cfg == nil {
		panic("config cannot be nil")
	}
	if rag == nil {
		panic("RAG service cannot be nil")
	}
	if logger == nil {
		panic("logger cannot be nil")
	}
	return &ReviewJob{cfg: cfg, ragService: rag, logger: logger}
}

// Run executes the code review job for a given GitHub event.
func (j *ReviewJob) Run(ctx context.Context, event *core.GitHubEvent) error {
	if err := j.validateInputs(ctx, event); err != nil {
		j.logger.Error("Input validation failed", "error", err)
		return fmt.Errorf("input validation failed: %w", err)
	}

	j.logger.Info("Starting review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	ghClient, err := github.CreateInstallationClient(ctx, j.cfg, event.InstallationID)
	if err != nil {
		j.logger.Error("Failed to create GitHub client", "error", err)
		return fmt.Errorf("failed to create GitHub client: %w", err)
	}

	pr, err := ghClient.GetPullRequest(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		j.logger.Error("Failed to get PR details", "error", err)
		return fmt.Errorf("failed to get PR details: %w", err)
	}
	if pr.GetHead() == nil || pr.GetHead().GetSHA() == "" {
		return fmt.Errorf("PR %d has no valid head SHA", event.PRNumber)
	}
	event.HeadSHA = pr.GetHead().GetSHA()

	statusUpdater := github.NewStatusUpdater(ghClient)
	checkRunID, err := statusUpdater.InProgress(ctx, event, "Code Review", "AI analysis in progress...")
	if err != nil {
		j.logger.Error("Failed to set in-progress status", "error", err)
		return fmt.Errorf("failed to set in-progress status: %w", err)
	}

	cloneCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cloner := gitutil.NewCloner(j.logger)
	repoPath, cleanup, err := cloner.Clone(cloneCtx, event.RepoCloneURL, event.HeadSHA)
	if err != nil {
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, "Failed to clone repository")
		return fmt.Errorf("failed to clone repository: %w", err)
	}
	defer cleanup()

	collectionName := j.generateCollectionName(event.RepoFullName, j.cfg.EmbedderModelName)
	if err := j.ragService.SetupRepoContext(ctx, collectionName, repoPath); err != nil {
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, "Failed to create repository embeddings")
		return fmt.Errorf("failed to setup repository context: %w", err)
	}

	review, err := j.ragService.GenerateReview(ctx, collectionName, event, ghClient)
	if err != nil {
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, "Failed to generate review")
		return fmt.Errorf("failed to generate review: %w", err)
	}
	if review == "" {
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, "Generated review is empty")
		return fmt.Errorf("generated review is empty")
	}

	if err := statusUpdater.PostReviewComment(ctx, event, review); err != nil {
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, "Failed to post review comment")
		return fmt.Errorf("failed to post review comment: %w", err)
	}

	if err := statusUpdater.Completed(ctx, event, checkRunID, "success", "Review Complete", "AI analysis finished successfully"); err != nil {
		j.logger.Error("Failed to update completion status", "error", err)
		return fmt.Errorf("failed to update completion status: %w", err)
	}

	j.logger.Info("Review job completed successfully", "repo", event.RepoFullName, "pr", event.PRNumber)
	return nil
}

// validateInputs ensures the event contains all required fields.
func (j *ReviewJob) validateInputs(ctx context.Context, event *core.GitHubEvent) error {
	if ctx == nil {
		return fmt.Errorf("context cannot be nil")
	}
	if event == nil {
		return fmt.Errorf("event cannot be nil")
	}
	if event.RepoOwner == "" {
		return fmt.Errorf("repository owner cannot be empty")
	}
	if event.RepoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if event.RepoFullName == "" {
		return fmt.Errorf("repository full name cannot be empty")
	}
	if event.RepoCloneURL == "" {
		return fmt.Errorf("repository clone URL cannot be empty")
	}
	if event.PRNumber <= 0 {
		return fmt.Errorf("pull request number must be positive, got: %d", event.PRNumber)
	}
	if event.InstallationID <= 0 {
		return fmt.Errorf("installation ID must be positive, got: %d", event.InstallationID)
	}
	return nil
}

// generateCollectionName builds a valid Qdrant collection name from repo and model info.
func (j *ReviewJob) generateCollectionName(repoFullName, embedderName string) string {
	safeRepoName := strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))
	safeEmbedderName := strings.ToLower(strings.Split(embedderName, ":")[0])

	reg := regexp.MustCompile("[^a-z0-9_-]+")
	safeRepoName = reg.ReplaceAllString(safeRepoName, "")
	safeEmbedderName = reg.ReplaceAllString(safeEmbedderName, "")

	collectionName := fmt.Sprintf("repo-%s-%s", safeRepoName, safeEmbedderName)

	if len(collectionName) > 255 {
		collectionName = collectionName[:255]
	}
	return collectionName
}

// updateStatusOnError sends a failure status to GitHub Check Runs.
func (j *ReviewJob) updateStatusOnError(ctx context.Context, statusUpdater github.StatusUpdater, event *core.GitHubEvent, checkRunID int64, message string) {
	if err := statusUpdater.Completed(ctx, event, checkRunID, "failure", "Review Failed", message); err != nil {
		j.logger.Error("Failed to update failure status", "error", err)
	}
}
