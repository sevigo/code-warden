package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
)

var (
	ErrConfigNotFound = errors.New("config file not found")
	ErrConfigParsing  = errors.New("config parsing failed")
)

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
	m, ok := mutex.(*sync.Mutex)
	if !ok {
		// This should never happen as we store *sync.Mutex
		return &sync.Mutex{}
	}
	return m
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
		return j.runReReview(ctx, event)
	default:
		return fmt.Errorf("unknown review type: %v", event.Type)
	}
}

// runFullReview handles the initial `/review` command.
func (j *ReviewJob) runFullReview(ctx context.Context, event *core.GitHubEvent) error {
	j.logger.Info("🚀 Starting Code Review", "repo", event.RepoFullName, "pr", event.PRNumber)
	return j.executeReviewWorkflow(ctx, event, "Code Review", "AI analysis in progress...")
}

// runReReview handles the `/rereview` command.
func (j *ReviewJob) runReReview(ctx context.Context, event *core.GitHubEvent) error {
	j.logger.Info("🔄 Starting Re-Review", "repo", event.RepoFullName, "pr", event.PRNumber)
	return j.executeReReviewWorkflow(ctx, event)
}

func (j *ReviewJob) executeReReviewWorkflow(ctx context.Context, event *core.GitHubEvent) (err error) {
	reviewEnv, err := j.setupReviewEnvironment(ctx, event, "Follow-up Review", "Re-analyzing PR...")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			j.updateStatusOnError(ctx, reviewEnv.statusUpdater, event, reviewEnv.checkRunID, err)
		}
	}()

	// 1. Fetch the latest review from the database
	lastReview, err := j.store.GetLatestReviewForPR(ctx, event.RepoFullName, event.PRNumber)
	if err != nil {
		j.logger.Warn("failed to fetch last review for re-review", "error", err)
		// Fallback: If no previous review, run a full review instead
		err = j.executeReviewWorkflow(ctx, event, "Code Review (Fallback)", "No previous review found, running full review...")
		return err
	}

	// 2. Fetch changed files for context
	changedFiles, err := reviewEnv.ghClient.GetChangedFiles(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		err = fmt.Errorf("failed to get changed files for re-review context: %w", err)
		return err
	}

	// 3. Generate Re-Review using RAG service
	structuredReview, _, err := j.ragService.GenerateReReview(ctx, reviewEnv.repo, event, lastReview, reviewEnv.ghClient, changedFiles)
	if err != nil {
		err = fmt.Errorf("failed to generate re-review: %w", err)
		return err
	}

	// 4. Validate and filter suggestions (Fix for 422 Unprocessable Entity)
	validLineMaps := make(map[string]map[int]struct{})
	for _, f := range changedFiles {
		validLineMaps[f.Filename] = github.ParseValidLinesFromPatch(f.Patch, j.logger)
	}

	inlineSuggestions, offDiffSuggestions := ValidateSuggestionsByLine(j.logger, structuredReview.Suggestions, validLineMaps)
	structuredReview.Suggestions = inlineSuggestions

	if len(offDiffSuggestions) > 0 {
		j.logger.Info("Hidden off-diff suggestions during re-review", "count", len(offDiffSuggestions))
	}

	// 5. Post the result
	if err = reviewEnv.statusUpdater.PostStructuredReview(ctx, event, structuredReview); err != nil {
		return fmt.Errorf("failed to post re-review comment: %w", err)
	}

	// Update reReviewContent for DB save
	reReviewContent := structuredReview.Summary

	// 5. Save the re-review as a new review record?
	// Yes, to maintain history.
	dbReview := &core.Review{
		RepoFullName:  event.RepoFullName,
		PRNumber:      event.PRNumber,
		HeadSHA:       event.HeadSHA,
		ReviewContent: reReviewContent,
	}
	if err = j.store.SaveReview(ctx, dbReview); err != nil {
		j.logger.Warn("failed to save re-review to database (failing to avoid inconsistent state)", "error", err)
		return fmt.Errorf("failed to save re-review: %w", err)
	}

	return reviewEnv.statusUpdater.Completed(ctx, event, reviewEnv.checkRunID, "success", "Re-Review Complete", "Follow-up analysis finished.")
}

func (j *ReviewJob) executeReviewWorkflow(ctx context.Context, event *core.GitHubEvent, title, summary string) (err error) {
	reviewEnv, err := j.setupReviewEnvironment(ctx, event, title, summary)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			j.updateStatusOnError(ctx, reviewEnv.statusUpdater, event, reviewEnv.checkRunID, err)
		}
	}()

	// Skip if this exact commit was already reviewed (prevents duplicate work on rapid webhook delivery).
	// Only for full reviews — re-reviews intentionally re-analyze the same SHA.
	if event.Type == core.FullReview {
		existing, _ := j.store.GetLatestReviewForPR(ctx, event.RepoFullName, event.PRNumber)
		if existing != nil && existing.HeadSHA == event.HeadSHA {
			j.logger.Info("Skipping review — same SHA already reviewed",
				"repo", event.RepoFullName, "pr", event.PRNumber, "sha", event.HeadSHA)
			// Mark check run as completed so the PR status doesn't stay pending
			_ = reviewEnv.statusUpdater.Completed(ctx, event, reviewEnv.checkRunID,
				"success", "Review Already Exists", "This commit was already reviewed.")
			return nil
		}
	}

	structuredReview, rawReview, validFiles, err := j.processRepository(ctx, event, reviewEnv)
	if err != nil {
		return err
	}

	return j.completeReview(ctx, event, reviewEnv, structuredReview, rawReview, validFiles)
}

type reviewEnvironment struct {
	ghClient      github.Client
	repo          *storage.Repository
	statusUpdater github.StatusUpdater
	checkRunID    int64
	updateResult  *core.UpdateResult
	repoConfig    *core.RepoConfig
}

// setupReviewEnvironment initializes clients, syncs the repo, and loads all necessary configs.
func (j *ReviewJob) setupReviewEnvironment(ctx context.Context, event *core.GitHubEvent, title, summary string) (*reviewEnvironment, error) {
	ghClient, ghToken, statusUpdater, checkRunID, err := j.setupReview(ctx, event, title, summary)
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
func (j *ReviewJob) processRepository(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment) (*core.StructuredReview, string, map[string]map[int]struct{}, error) {
	if err := j.updateVectorStoreAndSHA(ctx, env.repoConfig, env.repo, env.updateResult); err != nil {
		return nil, "", nil, err
	}

	// Fetch diff and changed files once — used for both validation and review generation
	diff, err := env.ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to get PR diff: %w", err)
	}

	changedFiles, err := env.ghClient.GetChangedFiles(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to get changed files for validation: %w", err)
	}

	validLineMaps := make(map[string]map[int]struct{})
	for _, f := range changedFiles {
		validLineMaps[f.Filename] = github.ParseValidLinesFromPatch(f.Patch, j.logger)
	}

	var structuredReview *core.StructuredReview
	var rawReview string

	if len(j.cfg.AI.ComparisonModels) > 0 {
		j.logger.Info("Starting consensus review", "models", j.cfg.AI.ComparisonModels)
		structuredReview, rawReview, err = j.ragService.GenerateConsensusReview(
			ctx,
			env.repoConfig,
			env.repo,
			event,
			j.cfg.AI.ComparisonModels,
			diff,
			changedFiles,
		)
	} else {
		structuredReview, rawReview, err = j.ragService.GenerateReview(
			ctx,
			env.repoConfig,
			env.repo,
			event,
			diff,
			changedFiles,
		)
	}
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to generate review: %w", err)
	}
	if structuredReview == nil || (structuredReview.Summary == "" && len(structuredReview.Suggestions) == 0) {
		// Log the raw review for debugging purposes
		j.logger.Error("generated review is empty or invalid", "raw_review", rawReview)
		return nil, "", nil, errors.New("generated review is empty or invalid")
	}

	return structuredReview, rawReview, validLineMaps, nil
}

// completeReview posts the review to GitHub, saves it to the DB, and marks the check run as successful.
func (j *ReviewJob) completeReview(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment, structuredReview *core.StructuredReview, rawReview string, validLineMaps map[string]map[int]struct{}) error {
	// Validate and filter suggestions to prevent 422 errors
	inlineSuggestions, offDiffSuggestions := ValidateSuggestionsByLine(j.logger, structuredReview.Suggestions, validLineMaps)
	structuredReview.Suggestions = inlineSuggestions

	// If there are off-diff suggestions, append them to the summary as "General Findings"
	if len(offDiffSuggestions) > 0 {
		// User requested to disable "General Findings" to reduce noise.
		// We log them for debug purposes instead.
		j.logger.Info("Hidden off-diff suggestions", "count", len(offDiffSuggestions))

		/*
			var offDiffBuilder strings.Builder
			offDiffBuilder.WriteString(structuredReview.Summary)
			offDiffBuilder.WriteString("\n\n---\n### 💡 General Findings (Outside Modified Lines)\n\n")
			offDiffBuilder.WriteString("The following issues were identified in parts of the code not directly modified in this PR:\n\n")
			for _, s := range offDiffSuggestions {
				offDiffBuilder.WriteString(fmt.Sprintf("*   **File:** `%s` (Line %d)\n", s.FilePath, s.LineNumber))
				offDiffBuilder.WriteString(fmt.Sprintf("    **Severity:** %s\n", s.Severity))
				offDiffBuilder.WriteString(fmt.Sprintf("    **Issue:** %s\n\n", s.Comment))
			}
			structuredReview.Summary = offDiffBuilder.String()
		*/
	}

	if err := env.statusUpdater.PostStructuredReview(ctx, event, structuredReview); err != nil {
		return fmt.Errorf("failed to post review comment to GitHub: %w", err)
	}

	dbReview := &core.Review{
		RepoFullName:  event.RepoFullName,
		PRNumber:      event.PRNumber,
		HeadSHA:       event.HeadSHA,
		ReviewContent: rawReview,
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
		j.logger.Info("⚠️ Initial indexing required (fresh clone or reset state)", "repo", repo.FullName)
		err := j.ragService.SetupRepoContext(ctx, repoConfig, repo, updateResult.RepoPath)
		if err != nil {
			return fmt.Errorf("failed to perform initial repository indexing: %w", err)
		}

	case len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0:
		j.logger.Info("⚡ Incremental update required", "repo", repo.FullName, "changed_files", len(updateResult.FilesToAddOrUpdate))
		err := j.ragService.UpdateRepoContext(ctx, repoConfig, repo, updateResult.RepoPath, updateResult.FilesToAddOrUpdate, updateResult.FilesToDelete)
		if err != nil {
			return fmt.Errorf("failed to update repository context in vector store: %w", err)
		}

	default:
		j.logger.Info("✅ Repository up-to-date. Skipping Scan.", "repo", repo.FullName)
	}

	if err := j.repoMgr.UpdateRepoSHA(ctx, repo.FullName, updateResult.HeadSHA); err != nil {
		j.logger.Error("CRITICAL: Vector store updated but failed to persist new SHA in database.",
			"error", err, "repo", repo.FullName, "new_sha", updateResult.HeadSHA)
		return fmt.Errorf("CRITICAL: failed to update last indexed SHA after vector store update: %w", err)
	}

	return nil
}

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

func (j *ReviewJob) loadAndProcessRepoConfig(repoPath, repoFullName string) *core.RepoConfig {
	repoConfig, configErr := config.LoadRepoConfig(repoPath)
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
