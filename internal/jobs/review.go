// Package jobs defines background tasks such as code reviews.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

var collectionNameRegexp = regexp.MustCompile("[^a-z0-9_-]+")

// ReviewJob performs AI-assisted code reviews.
type ReviewJob struct {
	cfg         *config.Config
	ragService  llm.RAGService
	reviewStore storage.Store
	repoStore   storage.Store
	logger      *slog.Logger
	repoPath    string
}

// NewReviewJob creates a new ReviewJob with all its dependencies.
func NewReviewJob(cfg *config.Config, rag llm.RAGService, reviewStore storage.Store, repoStore storage.Store, logger *slog.Logger, repoPath string) core.Job {
	if cfg == nil || rag == nil || reviewStore == nil || repoStore == nil || logger == nil || repoPath == "" {
		panic("NewReviewJob received a nil or empty dependency")
	}
	return &ReviewJob{cfg: cfg, ragService: rag, reviewStore: reviewStore, repoStore: repoStore, logger: logger, repoPath: repoPath}
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

// runFullReview handles the initial `/review` command.
func (j *ReviewJob) runFullReview(ctx context.Context, event *core.GitHubEvent) (err error) {
	j.logger.Info("Starting full review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	ghClient, ghToken, statusUpdater, checkRunID, err := j.setupReview(ctx, event, "Code Review", "AI analysis in progress...")
	if err != nil {
		return err
	}

	// Defer a handler to update GitHub status on any subsequent error.
	defer func() {
		if err != nil && statusUpdater != nil {
			j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, err)
		}
	}()

	// Get or create repository entry
	repo, err := j.repoStore.GetRepositoryByFullName(ctx, event.RepoFullName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to get repository from DB: %w", err)
	}

	var clonePath string
	collectionName := j.generateCollectionName(event.RepoFullName, j.cfg.EmbedderModelName)
	cloner := gitutil.NewCloner(j.logger)

	if repo == nil {
		// First time seeing this repo, clone it
		cloneCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		var cleanup func()
		clonePath, cleanup, err = cloner.Clone(cloneCtx, event.RepoCloneURL, event.HeadSHA, ghToken)
		if err != nil {
			return fmt.Errorf("failed to clone repository: %w", err)
		}
		defer cleanup()

		repo = &storage.Repository{
			FullName:             event.RepoFullName,
			ClonePath:            clonePath,
			QdrantCollectionName: collectionName,
			LastIndexedSHA:       event.HeadSHA,
		}
		if err := j.repoStore.CreateRepository(ctx, repo); err != nil {
			return fmt.Errorf("failed to create repository entry in DB: %w", err)
		}
	} else {
		// Repo already exists, update it
		clonePath = repo.ClonePath

		// Pull latest changes and calculate diff
		currentSHA := repo.LastIndexedSHA
		if err := cloner.Pull(ctx, clonePath, event.HeadSHA, ghToken); err != nil {
			return fmt.Errorf("failed to pull repository: %w", err)
		}

		// Calculate diff and update Qdrant
		added, modified, deleted, err := cloner.Diff(ctx, clonePath, currentSHA, event.HeadSHA)
		if err != nil {
			return fmt.Errorf("failed to calculate diff: %w", err)
		}

		if err := j.ragService.UpdateRepoContext(ctx, collectionName, clonePath, added, modified, deleted); err != nil {
			return fmt.Errorf("failed to update repository context: %w", err)
		}

		repo.LastIndexedSHA = event.HeadSHA
		if err := j.repoStore.UpdateRepository(ctx, repo); err != nil {
			return fmt.Errorf("failed to update repository entry in DB: %w", err)
		}
	}

	if err = j.ragService.SetupRepoContext(ctx, collectionName, clonePath); err != nil {
		return fmt.Errorf("failed to setup repository context: %w", err)
	}

	review, err := j.ragService.GenerateReview(ctx, collectionName, event, ghClient)
	if err != nil {
		return fmt.Errorf("failed to generate review: %w", err)
	}
	if strings.TrimSpace(review) == "" {
		return errors.New("generated review is empty")
	}

	if err = statusUpdater.PostReviewComment(ctx, event, review); err != nil {
		return fmt.Errorf("failed to post review comment: %w", err)
	}

	dbReview := &core.Review{
		RepoFullName:  event.RepoFullName,
		PRNumber:      event.PRNumber,
		HeadSHA:       event.HeadSHA,
		ReviewContent: review,
	}
	if err = j.reviewStore.SaveReview(ctx, dbReview); err != nil {
		j.logger.Error("failed to save review to database", "error", err)
		// We log this but don't fail the job, as the user has received the review.
	}

	if err = statusUpdater.Completed(ctx, event, checkRunID, "success", "Review Complete", "AI analysis finished."); err != nil {
		return fmt.Errorf("failed to update completion status: %w", err)
	}

	j.logger.Info("Full review job completed successfully")
	return nil
}

// runReReview handles the follow-up `/rereview` command.
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

	originalReview, err := j.reviewStore.GetLatestReviewForPR(ctx, event.RepoFullName, event.PRNumber)
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

// generateCollectionName builds a valid vector DB collection name from repo and model info.
func (j *ReviewJob) generateCollectionName(repoFullName, embedderName string) string {
	safeRepoName := strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))
	safeEmbedderName := strings.ToLower(strings.Split(embedderName, ":")[0])

	safeRepoName = collectionNameRegexp.ReplaceAllString(safeRepoName, "")
	safeEmbedderName = collectionNameRegexp.ReplaceAllString(safeEmbedderName, "")

	collectionName := fmt.Sprintf("repo-%s-%s", safeRepoName, safeEmbedderName)

	const maxCollectionNameLength = 255
	if len(collectionName) > maxCollectionNameLength {
		collectionName = collectionName[:maxCollectionNameLength]
	}
	return collectionName
}
