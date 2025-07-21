// Package jobs defines background tasks such as code reviews.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
)

// ReviewJob performs AI-assisted code reviews.
type ReviewJob struct {
	cfg        *config.Config
	ragService llm.RAGService
	store      storage.Store
	repoMgr    repomanager.RepoManager
	logger     *slog.Logger
}

// NewReviewJob creates a new ReviewJob with cleaner, more abstract dependencies.
func NewReviewJob(
	cfg *config.Config,
	rag llm.RAGService,
	store storage.Store,
	repoMgr repomanager.RepoManager,
	logger *slog.Logger,
) core.Job {
	return &ReviewJob{
		cfg:        cfg,
		ragService: rag,
		store:      store,
		repoMgr:    repoMgr,
		logger:     logger,
	}
}

// Run acts as a router, directing the event to the correct review flow.
func (j *ReviewJob) Run(ctx context.Context, event *core.GitHubEvent) error {
	if err := j.validateInputs(event); err != nil {
		j.logger.Error("Input validation failed", "error", err)
		return err
	}

	switch event.Type {
	case core.FullReview:
		return j.runFullReview(ctx, event)
	case core.ReReview:
		return j.runReReview(ctx, event)
	default:
		return fmt.Errorf("unknown review type: %v", event.Type)
	}
}

// runFullReview handles the initial `/review` command with the new, simplified logic.
func (j *ReviewJob) runFullReview(ctx context.Context, event *core.GitHubEvent) (err error) {
	j.logger.Info("Starting full review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	ghClient, ghToken, statusUpdater, checkRunID, err := j.setupReview(ctx, event, "Code Review", "AI analysis in progress...")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil && statusUpdater != nil {
			j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, err)
		}
	}()

	// Sync the repository using the manager. This single call handles both
	// the initial clone and subsequent incremental updates.
	updateResult, err := j.repoMgr.SyncRepo(ctx, event, ghToken)
	if err != nil {
		return fmt.Errorf("failed to sync repository: %w", err)
	}

	// Retrieve the repository record to get the correct collection name from the database.
	repoRecord, err := j.repoMgr.GetRepoRecord(ctx, event.RepoFullName)
	if err != nil {
		return fmt.Errorf("failed to retrieve repository record after sync: %w", err)
	}
	if repoRecord == nil {
		return fmt.Errorf("repository record is unexpectedly nil after sync for %s", event.RepoFullName)
	}
	collectionName := repoRecord.QdrantCollectionName

	// Update the vector store based on the results from the manager.
	if updateResult.IsInitialClone {
		// If it's the first time, perform a full indexing.
		err = j.ragService.SetupRepoContext(ctx, collectionName, updateResult.RepoPath)
		if err != nil {
			return fmt.Errorf("failed to perform initial repository indexing: %w", err)
		}
	} else if len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0 {
		// Otherwise, perform an incremental update.
		err = j.ragService.UpdateRepoContext(ctx, collectionName, updateResult.RepoPath, updateResult.FilesToAddOrUpdate, updateResult.FilesToDelete)
		if err != nil {
			return fmt.Errorf("failed to update repository context in vector store: %w", err)
		}
	} else {
		j.logger.Info("no file changes detected between SHAs, skipping vector store update", "repo", event.RepoFullName)
	}

	// After a successful vector store update, persist the new SHA in our database.
	if err := j.repoMgr.UpdateRepoSHA(ctx, event.RepoFullName, event.HeadSHA); err != nil {
		return fmt.Errorf("CRITICAL: failed to update last indexed SHA in database: %w", err)
	}

	// Generate the review using the now-up-to-date context.
	review, err := j.ragService.GenerateReview(ctx, collectionName, event, ghClient)
	if err != nil {
		return fmt.Errorf("failed to generate review: %w", err)
	}
	if strings.TrimSpace(review) == "" {
		return errors.New("generated review is empty")
	}

	// Post comment, save review record, and complete the check run.
	if err = statusUpdater.PostReviewComment(ctx, event, review); err != nil {
		return fmt.Errorf("failed to post review comment: %w", err)
	}
	dbReview := &core.Review{
		RepoFullName:  event.RepoFullName,
		PRNumber:      event.PRNumber,
		HeadSHA:       event.HeadSHA,
		ReviewContent: review,
	}
	if err = j.store.SaveReview(ctx, dbReview); err != nil {
		j.logger.Error("failed to save review to database", "error", err)
	}

	if err = statusUpdater.Completed(ctx, event, checkRunID, "success", "Review Complete", "AI analysis finished."); err != nil {
		return fmt.Errorf("failed to update completion status: %w", err)
	}

	j.logger.Info("Full review job completed successfully")
	return nil
}

// runReReview remains unchanged as its logic is correct.
func (j *ReviewJob) runReReview(ctx context.Context, event *core.GitHubEvent) (err error) {
	j.logger.Info("Starting re-review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	ghClient, _, statusUpdater, checkRunID, err := j.setupReview(ctx, event, "Follow-up Review", "Checking for fixes...")
	if err != nil {
		return err
	}

	defer func() {
		if err != nil && statusUpdater != nil {
			j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, err)
		}
	}()

	originalReview, err := j.store.GetLatestReviewForPR(ctx, event.RepoFullName, event.PRNumber)
	if err != nil {
		return fmt.Errorf("could not find a previous review to check against: %w", err)
	}

	followUp, err := j.ragService.GenerateReReview(ctx, event, originalReview, ghClient)
	if err != nil {
		return fmt.Errorf("failed to generate follow-up review: %w", err)
	}

	if err = statusUpdater.PostReviewComment(ctx, event, followUp); err != nil {
		return fmt.Errorf("failed to post follow-up comment: %w", err)
	}

	if err = statusUpdater.Completed(ctx, event, checkRunID, "success", "Follow-up Complete", "Analysis finished."); err != nil {
		return fmt.Errorf("failed to update completion status: %w", err)
	}

	j.logger.Info("Re-review job completed successfully")
	return nil
}

// setupReview initializes the GitHub client, gets PR details, and sets the initial status.
func (j *ReviewJob) setupReview(ctx context.Context, event *core.GitHubEvent, title, summary string) (ghClient github.Client, ghToken string, statusUpdater github.StatusUpdater, checkRunID int64, err error) {
	ghClient, ghToken, err = github.CreateInstallationClient(ctx, j.cfg, event.InstallationID, j.logger)
	if err != nil {
		err = fmt.Errorf("failed to create GitHub client: %w", err)
		return
	}

	pr, err := ghClient.GetPullRequest(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		err = fmt.Errorf("failed to get PR details: %w", err)
		return
	}
	if pr.GetHead() == nil || pr.GetHead().GetSHA() == "" {
		err = fmt.Errorf("PR #%d has no valid head SHA", event.PRNumber)
		return
	}
	event.HeadSHA = pr.GetHead().GetSHA()

	statusUpdater = github.NewStatusUpdater(ghClient)
	checkRunID, err = statusUpdater.InProgress(ctx, event, title, summary)
	if err != nil {
		err = fmt.Errorf("failed to set in-progress status: %w", err)
		return
	}

	return
}

// updateStatusOnError logs the job error and updates the GitHub check run.
func (j *ReviewJob) updateStatusOnError(ctx context.Context, statusUpdater github.StatusUpdater, event *core.GitHubEvent, checkRunID int64, jobErr error) {
	j.logger.Error("Review job step failed", "error", jobErr, "repo", event.RepoFullName, "pr", event.PRNumber)
	if err := statusUpdater.Completed(ctx, event, checkRunID, "failure", "Review Failed", jobErr.Error()); err != nil {
		j.logger.Error("Failed to update failure status on GitHub", "original_error", jobErr, "status_update_error", err)
	}
}

// validateInputs ensures the event contains all required fields.
func (j *ReviewJob) validateInputs(event *core.GitHubEvent) error {
	if event == nil {
		return errors.New("event cannot be nil")
	}

	switch {
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
