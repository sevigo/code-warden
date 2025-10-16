package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
)

// ReviewJob performs AI-assisted code reviews.
type ReviewJob struct {
	cfg         *config.Config
	ragService  llm.RAGService
	store       storage.Store
	repoMgr     repomanager.RepoManager
	logger      *slog.Logger
	repoMutexes sync.Map
}

// NewReviewJob creates a new ReviewJob.
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

// getRepoMutex returns a mutex for the given repository to prevent concurrent operations.
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

	mutex := j.getRepoMutex(event.RepoFullName)
	mutex.Lock()
	defer mutex.Unlock()

	switch event.Type {
	case core.FullReview:
		return j.runFullReview(ctx, event)
	case core.ReReview:
		return j.handleUnsupportedReReview(ctx, event)
	default:
		return fmt.Errorf("unknown review type: %v", event.Type)
	}
}

// runFullReview handles the initial `/review` command.
func (j *ReviewJob) runFullReview(ctx context.Context, event *core.GitHubEvent) (err error) {
	j.logger.Info("Starting full review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	// Setup the review environment.
	reviewEnv, err := j.setupReviewEnvironment(ctx, event)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// If any subsequent step fails, update the GitHub check run to failure.
			j.updateStatusOnError(ctx, reviewEnv.statusUpdater, event, reviewEnv.checkRunID, err)
		}
	}()

	// Process repository changes (indexing) and generate the AI review.
	rawReviewJSON, err := j.processRepository(ctx, event, reviewEnv)
	if err != nil {
		return err
	}

	// Post the review to GitHub and mark the job as complete.
	return j.completeReview(ctx, event, reviewEnv, rawReviewJSON)
}

// reviewEnvironment holds all resources needed for a single review job.
type reviewEnvironment struct {
	ghClient      github.Client
	repo          *storage.Repository // Contains collection name, embedder model, etc.
	statusUpdater github.StatusUpdater
	checkRunID    int64
	updateResult  *core.UpdateResult
	repoConfig    *core.RepoConfig
}

// setupReviewEnvironment initializes clients, syncs the repo, and loads all necessary configs.
func (j *ReviewJob) setupReviewEnvironment(ctx context.Context, event *core.GitHubEvent) (*reviewEnvironment, error) {
	ghClient, ghToken, statusUpdater, checkRunID, err := j.setupReview(ctx, event, "Code Review", "AI analysis in progress...")
	if err != nil {
		return nil, err
	}

	updateResult, err := j.repoMgr.SyncRepo(ctx, event, ghToken)
	if err != nil {
		err = fmt.Errorf("failed to sync repository: %w", err)
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, err)
		return nil, err
	}

	repo, err := j.repoMgr.GetRepoRecord(ctx, event.RepoFullName)
	if err != nil || repo == nil {
		err = fmt.Errorf("failed to retrieve repository record after sync for %s: %w", event.RepoFullName, err)
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, err)
		return nil, err
	}

	repoConfig := j.loadAndProcessRepoConfig(updateResult.RepoPath, event.RepoFullName)

	return &reviewEnvironment{
		ghClient:      ghClient,
		repo:          repo,
		statusUpdater: statusUpdater,
		checkRunID:    checkRunID,
		updateResult:  updateResult,
		repoConfig:    repoConfig,
	}, nil
}

// processRepository handles vector store updates and the actual review generation.
func (j *ReviewJob) processRepository(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment) (string, error) {
	if err := j.updateVectorStoreAndSHA(ctx, env.repoConfig, env.repo, env.updateResult); err != nil {
		return "", err
	}

	// The RAGService is expected to take the full repository object.
	structuredReview, rawReviewJSON, err := j.ragService.GenerateReview(
		ctx,
		env.repoConfig,
		env.repo, // Pass the entire repo object
		event,
		env.ghClient,
	)
	if err != nil {
		return "", fmt.Errorf("failed to generate review: %w", err)
	}
	if structuredReview == nil || (structuredReview.Summary == "" && len(structuredReview.Suggestions) == 0) {
		return "", errors.New("generated review is empty or invalid")
	}

	return rawReviewJSON, nil
}

// completeReview posts the review to GitHub, saves it to the DB, and marks the check run as successful.
func (j *ReviewJob) completeReview(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment, rawReviewJSON string) error {
	var structuredReview core.StructuredReview
	if err := json.Unmarshal([]byte(rawReviewJSON), &structuredReview); err != nil {
		return fmt.Errorf("internal error: failed to re-parse generated review JSON: %w", err)
	}

	if err := env.statusUpdater.PostStructuredReview(ctx, event, &structuredReview); err != nil {
		return fmt.Errorf("failed to post review comment to GitHub: %w", err)
	}

	dbReview := &core.Review{
		RepoFullName:  event.RepoFullName,
		PRNumber:      event.PRNumber,
		HeadSHA:       event.HeadSHA,
		ReviewContent: rawReviewJSON,
	}
	if err := j.store.SaveReview(ctx, dbReview); err != nil {
		j.logger.Error("failed to save review to database", "error", err)
		return fmt.Errorf("failed to save review record to database: %w", err)
	}

	if err := env.statusUpdater.Completed(ctx, event, env.checkRunID, "success", "Review Complete", "AI analysis finished."); err != nil {
		return fmt.Errorf("failed to update completion status on GitHub: %w", err)
	}

	j.logger.Info("Full review job completed successfully")
	return nil
}

// updateVectorStoreAndSHA performs the indexing of changed files.
func (j *ReviewJob) updateVectorStoreAndSHA(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, updateResult *core.UpdateResult) error {
	switch {
	case updateResult.IsInitialClone:
		j.logger.Info("Performing initial repository indexing", "repo", repo.FullName)
		err := j.ragService.SetupRepoContext(ctx, repoConfig, repo.QdrantCollectionName, repo.EmbedderModelName, updateResult.RepoPath)
		if err != nil {
			return fmt.Errorf("failed to perform initial repository indexing: %w", err)
		}

	case len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0:
		j.logger.Info("Performing incremental repository indexing", "repo", repo.FullName)
		err := j.ragService.UpdateRepoContext(ctx, repoConfig, repo, updateResult.RepoPath, updateResult.FilesToAddOrUpdate, updateResult.FilesToDelete)
		if err != nil {
			return fmt.Errorf("failed to update repository context in vector store: %w", err)
		}

	default:
		j.logger.Info("No file changes detected, skipping vector store update", "repo", repo.FullName)
	}

	if err := j.repoMgr.UpdateRepoSHA(ctx, repo.FullName, updateResult.HeadSHA); err != nil {
		j.logger.Error("CRITICAL: Vector store updated but failed to persist new SHA in database.",
			"error", err, "repo", repo.FullName, "new_sha", updateResult.HeadSHA)
		return fmt.Errorf("CRITICAL: failed to update last indexed SHA after vector store update: %w", err)
	}

	return nil
}

// --- Utility and Other Functions ---

func (j *ReviewJob) setupReview(ctx context.Context, event *core.GitHubEvent, title, summary string) (github.Client, string, github.StatusUpdater, int64, error) {
	ghClient, ghToken, err := github.CreateInstallationClient(ctx, j.cfg, event.InstallationID, j.logger)
	if err != nil {
		return nil, "", nil, 0, fmt.Errorf("failed to create GitHub client: %w", err)
	}

	pr, err := ghClient.GetPullRequest(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, "", nil, 0, fmt.Errorf("failed to get PR details: %w", err)
	}
	if pr.GetHead() == nil || pr.GetHead().GetSHA() == "" {
		return nil, "", nil, 0, fmt.Errorf("PR #%d has no valid head SHA", event.PRNumber)
	}
	event.HeadSHA = pr.GetHead().GetSHA()

	statusUpdater := github.NewStatusUpdater(ghClient)
	checkRunID, err := statusUpdater.InProgress(ctx, event, title, summary)
	if err != nil {
		return nil, "", nil, 0, fmt.Errorf("failed to set in-progress status: %w", err)
	}

	return ghClient, ghToken, statusUpdater, checkRunID, nil
}

func (j *ReviewJob) updateStatusOnError(ctx context.Context, statusUpdater github.StatusUpdater, event *core.GitHubEvent, checkRunID int64, jobErr error) {
	j.logger.Error("Review job step failed", "error", jobErr, "repo", event.RepoFullName)
	if statusUpdater != nil && checkRunID > 0 {
		if err := statusUpdater.Completed(ctx, event, checkRunID, "failure", "Review Failed", jobErr.Error()); err != nil {
			j.logger.Error("Failed to update failure status on GitHub", "original_error", jobErr, "status_update_error", err)
		}
	}
}

func (j *ReviewJob) handleUnsupportedReReview(ctx context.Context, event *core.GitHubEvent) error {
	j.logger.Info("Handling temporarily disabled /rereview command", "repo", event.RepoFullName)
	_, _, statusUpdater, checkRunID, err := j.setupReview(ctx, event, "Follow-up Review", "Preparing for follow-up...")
	if err != nil {
		return err
	}

	comment := "The `/rereview` command is being upgraded and is temporarily unavailable. Please use `/review` for a full new analysis."
	if postErr := statusUpdater.PostSimpleComment(ctx, event, comment); postErr != nil {
		j.logger.Error("Failed to post comment for disabled feature", "error", postErr)
	}

	summary := "The `/rereview` command is temporarily disabled while it's being upgraded."
	if completeErr := statusUpdater.Completed(ctx, event, checkRunID, "neutral", "Feature Unavailable", summary); completeErr != nil {
		return fmt.Errorf("failed to update completion status: %w", completeErr)
	}
	return nil
}

func (j *ReviewJob) validateInputs(event *core.GitHubEvent) error {
	if event == nil {
		return errors.New("event cannot be nil")
	}
	if event.RepoOwner == "" || event.RepoName == "" || event.RepoFullName == "" || event.RepoCloneURL == "" {
		return errors.New("repository information cannot be empty")
	}
	if event.PRNumber <= 0 {
		return fmt.Errorf("pull request number must be positive, got: %d", event.PRNumber)
	}
	if event.InstallationID <= 0 {
		return fmt.Errorf("installation ID must be positive, got: %d", event.InstallationID)
	}
	return nil
}

var (
	ErrConfigNotFound = errors.New("config file not found")
	ErrConfigParsing  = errors.New("config parsing failed")
)

func (j *ReviewJob) loadAndProcessRepoConfig(repoPath, repoFullName string) *core.RepoConfig {
	repoConfig, configErr := j.loadRepoConfig(repoPath)
	if configErr != nil {
		if errors.Is(configErr, ErrConfigNotFound) {
			j.logger.Info("no .code-warden.yml found, using defaults", "repo", repoFullName)
		} else {
			j.logger.Warn("failed to parse .code-warden.yml, using defaults", "error", configErr, "repo", repoFullName)
		}
		return core.DefaultRepoConfig()
	}
	return repoConfig
}

func (j *ReviewJob) loadRepoConfig(repoPath string) (*core.RepoConfig, error) {
	configPath := filepath.Join(repoPath, ".code-warden.yml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return core.DefaultRepoConfig(), ErrConfigNotFound
		}
		return nil, fmt.Errorf("failed to read .code-warden.yml: %w", err)
	}
	config := core.DefaultRepoConfig()
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfigParsing, err)
	}
	return config, nil
}
