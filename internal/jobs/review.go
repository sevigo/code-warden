// Package jobs defines background tasks such as code reviews.
package jobs

import (
	"context"
	"errors"
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
	"github.com/sevigo/code-warden/internal/storage"
)

// collectionNameRegexp is compiled once at package level for efficiency.
var collectionNameRegexp = regexp.MustCompile("[^a-z0-9_-]+")

// ReviewJob is a background job that performs AI-assisted code reviews.
type ReviewJob struct {
	cfg         *config.Config
	ragService  llm.RAGService
	logger      *slog.Logger
	reviewStore storage.Store
}

// NewReviewJob creates a new ReviewJob with config, RAG service, and logger.
func NewReviewJob(cfg *config.Config, rag llm.RAGService, reviewStore storage.Store, logger *slog.Logger) core.Job {
	return &ReviewJob{
		cfg:         cfg,
		ragService:  rag,
		logger:      logger,
		reviewStore: reviewStore,
	}
}

// Run orchestrates the code review job for a given GitHub event.
// It handles setup, execution, status updates, and error handling.
//
//nolint:nonamedreturns // A named return is used here to inspect the error in a defer block.
func (j *ReviewJob) Run(ctx context.Context, event *core.GitHubEvent) (err error) {
	if validationErr := j.validateInputs(ctx, event); validationErr != nil {
		j.logger.Error("Input validation failed", "error", validationErr)
		return fmt.Errorf("input validation failed: %w", validationErr)
	}

	j.logger.Info("Starting review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	ghClient, ghToken, err := github.CreateInstallationClient(ctx, j.cfg, event.InstallationID, j.logger)
	if err != nil {
		return fmt.Errorf("failed to create GitHub client: %w", err)
	}
	statusUpdater := github.NewStatusUpdater(ghClient)
	checkRunID, err := statusUpdater.InProgress(ctx, event, "Code Review", "AI analysis in progress...")
	if err != nil {
		return fmt.Errorf("failed to set in-progress status: %w", err)
	}

	// Defer a centralized error handler that updates the GitHub status on failure.
	defer func() {
		if err != nil {
			j.handleError(ctx, statusUpdater, event, checkRunID, err)
		}
	}()

	// Fetch PR details and perform the core review process.
	review, err := j.processPullRequest(ctx, event, ghClient, ghToken)
	if err != nil {
		return err
	}

	// Finalize the review by posting comments and setting the success status.
	if err = j.finalizeSuccess(ctx, statusUpdater, event, checkRunID, review); err != nil {
		return err
	}

	j.logger.Info("Review job completed successfully", "repo", event.RepoFullName, "pr", event.PRNumber)
	return nil
}

// processPullRequest contains the core logic for reviewing a pull request.
// It fetches PR data, clones the repo, generates embeddings, and produces the review.
func (j *ReviewJob) processPullRequest(ctx context.Context, event *core.GitHubEvent, ghClient github.Client, ghToken string) (string, error) {
	pr, err := ghClient.GetPullRequest(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return "", fmt.Errorf("failed to get PR details: %w", err)
	}
	if pr.GetHead() == nil || pr.GetHead().GetSHA() == "" {
		return "", fmt.Errorf("PR %d has no valid head SHA", event.PRNumber)
	}
	event.HeadSHA = pr.GetHead().GetSHA()

	// Clone repository with a timeout.
	cloneCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cloner := gitutil.NewCloner(j.logger)
	repoPath, cleanup, err := cloner.Clone(cloneCtx, event.RepoCloneURL, event.HeadSHA, ghToken)
	if err != nil {
		return "", fmt.Errorf("failed to clone repository: %w", err)
	}
	defer cleanup()

	// Setup RAG context and generate review.
	collectionName := j.generateCollectionName(event.RepoFullName, j.cfg.EmbedderModelName)
	if err := j.ragService.SetupRepoContext(ctx, collectionName, repoPath); err != nil {
		return "", fmt.Errorf("failed to setup repository context: %w", err)
	}

	review, err := j.ragService.GenerateReview(ctx, collectionName, event, ghClient)
	if err != nil {
		return "", fmt.Errorf("failed to generate review: %w", err)
	}
	if review == "" {
		return "", errors.New("generated review is empty")
	}

	return review, nil
}

// finalizeSuccess handles the successful completion of a review.
// It posts the review comment, saves it to storage, and updates the GitHub status.
func (j *ReviewJob) finalizeSuccess(ctx context.Context, statusUpdater github.StatusUpdater, event *core.GitHubEvent, checkRunID int64, reviewContent string) error {
	if err := statusUpdater.PostReviewComment(ctx, event, reviewContent); err != nil {
		return fmt.Errorf("failed to post review comment: %w", err)
	}

	dbReview := &core.Review{
		RepoFullName:  event.RepoFullName,
		PRNumber:      event.PRNumber,
		HeadSHA:       event.HeadSHA,
		ReviewContent: reviewContent,
	}
	if err := j.reviewStore.SaveReview(ctx, dbReview); err != nil {
		// Log the error but don't fail the entire job, as the user-facing part is done.
		j.logger.Error("failed to save review to database", "error", err)
	}

	if err := statusUpdater.Completed(ctx, event, checkRunID, "success", "Review Complete", "AI analysis finished successfully"); err != nil {
		return fmt.Errorf("failed to update completion status: %w", err)
	}
	return nil
}

// handleError updates the GitHub check run to a "failure" state.
func (j *ReviewJob) handleError(ctx context.Context, statusUpdater github.StatusUpdater, event *core.GitHubEvent, checkRunID int64, jobErr error) {
	j.logger.Error("Review job failed", "error", jobErr, "repo", event.RepoFullName, "pr", event.PRNumber)
	message := jobErr.Error()
	if err := statusUpdater.Completed(ctx, event, checkRunID, "failure", "Review Failed", message); err != nil {
		j.logger.Error("Failed to update failure status", "error", err)
	}
}

// validateInputs ensures the event contains all required fields.
func (j *ReviewJob) validateInputs(ctx context.Context, event *core.GitHubEvent) error {
	if event == nil {
		return errors.New("event cannot be nil")
	}
	// A switch statement can be cleaner for multiple simple checks.
	switch {
	case ctx == nil:
		return errors.New("context cannot be nil")
	case event.RepoOwner == "":
		return errors.New("repository owner cannot be empty")
	case event.RepoName == "":
		return errors.New("repository name cannot be empty")
	case event.RepoFullName == "":
		return errors.New("repository full name cannot be empty")
	case event.RepoCloneURL == "":
		return errors.New("repository clone URL cannot be empty")
	case event.PRNumber <= 0:
		return fmt.Errorf("pull request number must be positive, got: %d", event.PRNumber)
	case event.InstallationID <= 0:
		return fmt.Errorf("installation ID must be positive, got: %d", event.InstallationID)
	}
	return nil
}

// generateCollectionName builds a valid vector DB collection name from repo and model info.
func (j *ReviewJob) generateCollectionName(repoFullName, embedderName string) string {
	safeRepoName := strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))
	safeEmbedderName := strings.ToLower(strings.Split(embedderName, ":")[0])

	// Use the pre-compiled package-level regexp
	safeRepoName = collectionNameRegexp.ReplaceAllString(safeRepoName, "")
	safeEmbedderName = collectionNameRegexp.ReplaceAllString(safeEmbedderName, "")

	collectionName := fmt.Sprintf("repo-%s-%s", safeRepoName, safeEmbedderName)

	const maxCollectionNameLength = 255
	if len(collectionName) > maxCollectionNameLength {
		collectionName = collectionName[:maxCollectionNameLength]
	}
	return collectionName
}
