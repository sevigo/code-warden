package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
)

const (
	// Maximum size for review comments (GitHub's limit is ~65KB, we use 60KB to be safe)
	maxReviewSize = 60 * 1024
)

// ReviewJob performs AI-assisted code reviews.
type ReviewJob struct {
	cfg        *config.Config
	ragService llm.RAGService
	store      storage.Store
	repoMgr    repomanager.RepoManager
	logger     *slog.Logger

	// Mutex map to prevent concurrent operations on the same repository
	repoMutexes sync.Map
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
		cfg:         cfg,
		ragService:  rag,
		store:       store,
		repoMgr:     repoMgr,
		logger:      logger,
		repoMutexes: sync.Map{},
	}
}

// getRepoMutex returns a mutex for the given repository to prevent concurrent operations
func (j *ReviewJob) getRepoMutex(repoFullName string) *sync.Mutex {
	mutex, _ := j.repoMutexes.LoadOrStore(repoFullName, &sync.Mutex{})
	return mutex.(*sync.Mutex)
}

// Run acts as a router, directing the event to the correct review flow.
func (j *ReviewJob) Run(ctx context.Context, event *core.GitHubEvent) error {
	if err := j.validateInputs(event); err != nil {
		j.logger.Error("Input validation failed", "error", err)
		return err
	}

	// Acquire repository-specific mutex to prevent concurrent operations
	mutex := j.getRepoMutex(event.RepoFullName)
	mutex.Lock()
	defer mutex.Unlock()

	switch event.Type {
	case core.FullReview:
		return j.runFullReview(ctx, event)
	case core.ReReview:
		return j.runReReview(ctx, event)
	default:
		return fmt.Errorf("unknown review type: %v", event.Type)
	}
}

// checkContext verifies if the context is still valid
func (j *ReviewJob) checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// runFullReview handles the initial `/review` command with the new, simplified logic.
func (j *ReviewJob) runFullReview(ctx context.Context, event *core.GitHubEvent) (err error) {
	j.logger.Info("Starting full review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	if err := j.checkContext(ctx); err != nil {
		return fmt.Errorf("context cancelled before starting: %w", err)
	}

	ghClient, ghToken, statusUpdater, checkRunID, err := j.setupReview(ctx, event, "Code Review", "AI analysis in progress...")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil && statusUpdater != nil {
			j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, err)
		}
	}()

	// Check context after setup
	if err := j.checkContext(ctx); err != nil {
		return fmt.Errorf("context cancelled after setup: %w", err)
	}

	// Sync the repository using the manager. This single call handles both
	// the initial clone and subsequent incremental updates.
	updateResult, err := j.repoMgr.SyncRepo(ctx, event, ghToken)
	if err != nil {
		return fmt.Errorf("failed to sync repository: %w", err)
	}

	// Ensure cleanup of any temporary resources
	defer func() {
		if updateResult != nil && updateResult.RepoPath != "" {
			j.cleanupRepoResources(updateResult.RepoPath)
		}
	}()

	// Check context after repo sync
	if err := j.checkContext(ctx); err != nil {
		return fmt.Errorf("context cancelled after repo sync: %w", err)
	}

	repoConfig, configErr := j.loadRepoConfig(updateResult.RepoPath)
	if configErr != nil {
		if errors.Is(configErr, ErrConfigNotFound) {
			j.logger.Info("no .code-warden.yml found, using defaults", "repo", event.RepoFullName)
			repoConfig = core.DefaultRepoConfig()
		} else {
			j.logger.Warn("failed to parse .code-warden.yml, using defaults", "error", configErr, "repo", event.RepoFullName)
			repoConfig = core.DefaultRepoConfig()
		}
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

	// Check context before vector store operations
	if err := j.checkContext(ctx); err != nil {
		return fmt.Errorf("context cancelled before vector store update: %w", err)
	}

	// Update the vector store and database SHA atomically
	if err := j.updateVectorStoreAndSHA(ctx, event, repoConfig, collectionName, updateResult); err != nil {
		return err
	}

	// Check context before generating review
	if err := j.checkContext(ctx); err != nil {
		return fmt.Errorf("context cancelled before review generation: %w", err)
	}

	// Generate the review using the now-up-to-date context.
	review, err := j.ragService.GenerateReview(ctx, repoConfig, collectionName, event, ghClient)
	if err != nil {
		return fmt.Errorf("failed to generate review: %w", err)
	}
	if strings.TrimSpace(review) == "" {
		return errors.New("generated review is empty")
	}

	// Validate review size before posting
	if err := j.validateReviewSize(review); err != nil {
		return err
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
		// Don't return error here as the review was already posted
	}

	if err = statusUpdater.Completed(ctx, event, checkRunID, "success", "Review Complete", "AI analysis finished."); err != nil {
		return fmt.Errorf("failed to update completion status: %w", err)
	}

	j.logger.Info("Full review job completed successfully")
	return nil
}

// updateVectorStoreAndSHA performs atomic-like update of vector store and SHA
func (j *ReviewJob) updateVectorStoreAndSHA(ctx context.Context, event *core.GitHubEvent, repoConfig *core.RepoConfig, collectionName string, updateResult *repomanager.UpdateResult) error {
	var vectorStoreUpdated bool

	// Update the vector store based on the results from the manager.
	switch {
	case updateResult.IsInitialClone:
		// If it's the first time, perform a full indexing.
		err := j.ragService.SetupRepoContext(ctx, repoConfig, collectionName, updateResult.RepoPath)
		if err != nil {
			return fmt.Errorf("failed to perform initial repository indexing: %w", err)
		}
		vectorStoreUpdated = true

	case len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0:
		// Otherwise, perform an incremental update.
		err := j.ragService.UpdateRepoContext(ctx, repoConfig, collectionName, updateResult.RepoPath, updateResult.FilesToAddOrUpdate, updateResult.FilesToDelete)
		if err != nil {
			return fmt.Errorf("failed to update repository context in vector store: %w", err)
		}
		vectorStoreUpdated = true

	default:
		j.logger.Info("no file changes detected between SHAs, skipping vector store update", "repo", event.RepoFullName)
	}

	// Only update the SHA if we successfully updated the vector store
	if vectorStoreUpdated {
		if err := j.repoMgr.UpdateRepoSHA(ctx, event.RepoFullName, event.HeadSHA); err != nil {
			// This is critical - we need to log extensively and potentially rollback
			j.logger.Error("CRITICAL: vector store updated but failed to update SHA in database - data inconsistency detected",
				"error", err,
				"repo", event.RepoFullName,
				"head_sha", event.HeadSHA,
			)
			// TODO: Consider implementing rollback mechanism for vector store
			return fmt.Errorf("CRITICAL: failed to update last indexed SHA in database after vector store update: %w", err)
		}
	}

	return nil
}

// validateReviewSize checks if the review content is within acceptable limits
func (j *ReviewJob) validateReviewSize(review string) error {
	if len(review) > maxReviewSize {
		truncatedReview := review[:maxReviewSize-100] + "\n\n[Review truncated due to size limits]"
		j.logger.Warn("Review content too large, truncating",
			"original_size", len(review),
			"max_size", maxReviewSize,
		)
		// In a real implementation, you might want to modify the review content
		// For now, we'll return an error to indicate the issue
		return fmt.Errorf("review content too large (%d bytes), maximum allowed is %d bytes", len(review), maxReviewSize)
	}
	return nil
}

// cleanupRepoResources performs cleanup of temporary repository resources
func (j *ReviewJob) cleanupRepoResources(repoPath string) {
	// This is a placeholder for cleanup logic
	// The actual implementation would depend on what resources need cleanup
	j.logger.Debug("Cleaning up repository resources", "repo_path", repoPath)
	// Example: remove temporary files, close file handles, etc.
}

// runReReview remains largely unchanged but with added context checks and validation
func (j *ReviewJob) runReReview(ctx context.Context, event *core.GitHubEvent) (err error) {
	j.logger.Info("Starting re-review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	if err := j.checkContext(ctx); err != nil {
		return fmt.Errorf("context cancelled before starting: %w", err)
	}

	ghClient, _, statusUpdater, checkRunID, err := j.setupReview(ctx, event, "Follow-up Review", "Checking for fixes...")
	if err != nil {
		return err
	}

	defer func() {
		if err != nil && statusUpdater != nil {
			j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, err)
		}
	}()

	if err := j.checkContext(ctx); err != nil {
		return fmt.Errorf("context cancelled after setup: %w", err)
	}

	originalReview, err := j.store.GetLatestReviewForPR(ctx, event.RepoFullName, event.PRNumber)
	if err != nil {
		return fmt.Errorf("could not find a previous review to check against: %w", err)
	}

	if err := j.checkContext(ctx); err != nil {
		return fmt.Errorf("context cancelled before generating follow-up: %w", err)
	}

	followUp, err := j.ragService.GenerateReReview(ctx, event, originalReview, ghClient)
	if err != nil {
		return fmt.Errorf("failed to generate follow-up review: %w", err)
	}

	// Validate follow-up size
	if err := j.validateReviewSize(followUp); err != nil {
		return err
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

// Custom error types for better error handling
var (
	ErrConfigNotFound = errors.New("config file not found")
	ErrConfigParsing  = errors.New("config parsing failed")
)

func (j *ReviewJob) loadRepoConfig(repoPath string) (*core.RepoConfig, error) {
	configPath := filepath.Join(repoPath, ".code-warden.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist, which is fine. Return a specific error.
			return core.DefaultRepoConfig(), ErrConfigNotFound
		}
		// Some other error reading the file.
		return nil, fmt.Errorf("failed to read .code-warden.yml: %w", err)
	}

	config := core.DefaultRepoConfig()
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfigParsing, err)
	}

	j.logger.Info(".code-warden.yml loaded successfully", "repo_path", repoPath)
	return config, nil
}
g