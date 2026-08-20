package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/agent/reviewtools"
	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/code-warden/internal/stringsutil"

	"github.com/sevigo/goframe/llms"
)

type ReviewJob struct {
	cfg         *config.Config
	store       storage.Store
	repoMgr     repomanager.RepoManager
	logger      *slog.Logger
	llm         llms.Model
	promptMgr   *llm.PromptManager
	repoMutexes sync.Map
}

// NewReviewJob creates a new ReviewJob.
func NewReviewJob(
	cfg *config.Config,
	store storage.Store,
	repoMgr repomanager.RepoManager,
	logger *slog.Logger,
	model llms.Model,
	promptMgr *llm.PromptManager,
) *ReviewJob {
	return &ReviewJob{
		cfg:       cfg,
		store:     store,
		repoMgr:   repoMgr,
		logger:    logger,
		llm:       model,
		promptMgr: promptMgr,
	}
}

// getRepoMutex returns a mutex for the given repository to prevent concurrent operations.
func (j *ReviewJob) getRepoMutex(repoFullName string) *sync.Mutex {
	mutex, _ := j.repoMutexes.LoadOrStore(repoFullName, &sync.Mutex{})
	m, ok := mutex.(*sync.Mutex)
	if !ok {
		// This should never happen as we store *sync.Mutex, but log and recover
		j.logger.Error("type assertion failed for repo mutex", "repo", repoFullName, "type", fmt.Sprintf("%T", mutex))
		return &sync.Mutex{}
	}
	return m
}

// Run executes a code review for the supplied pull request event.
func (j *ReviewJob) Run(ctx context.Context, event *core.GitHubEvent) error {
	j.logger.Info("processing GitHub event",
		"repo", event.RepoFullName,
		"pr", event.PRNumber,
		"commenter", event.Commenter)

	if err := j.validateInputs(event); err != nil {
		j.logger.Error("Input validation failed", "error", err)
		return err
	}

	return j.runFullReview(ctx, event)
}

// runFullReview handles the initial `/review` command.
func (j *ReviewJob) runFullReview(ctx context.Context, event *core.GitHubEvent) error {
	j.logger.Info("🚀 Starting Code Review", "repo", event.RepoFullName, "pr", event.PRNumber)
	finish := j.startJobRun(ctx, "review", event, "webhook:/review")
	err := j.executeReviewWorkflow(ctx, event, "Code Review", "AI analysis in progress...")
	finish(ctx, err)
	return err
}

// startJobRun records a job as "running" and returns a function to finalize it.
func (j *ReviewJob) startJobRun(ctx context.Context, jobType string, event *core.GitHubEvent, triggeredBy string) func(context.Context, error) {
	startedAt := time.Now()
	jobID, err := j.store.InsertJobRun(ctx, &storage.JobRun{
		Type:         jobType,
		RepoFullName: event.RepoFullName,
		PRNumber:     event.PRNumber,
		Status:       "running",
		TriggeredBy:  triggeredBy,
		TriggeredAt:  startedAt,
	})
	if err != nil {
		j.logger.Warn("failed to record job run start", "type", jobType, "error", err)
		jobID = 0
	}
	return func(ctx context.Context, runErr error) {
		if jobID == 0 {
			return
		}
		status := "completed"
		if runErr != nil {
			status = "failed"
		}
		completedAt := time.Now()
		if updateErr := j.store.UpdateJobRun(ctx, jobID, status, completedAt, completedAt.Sub(startedAt).Milliseconds()); updateErr != nil {
			j.logger.Warn("failed to update job run", "id", jobID, "error", updateErr)
		}
	}
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

	// Skip if this exact commit was already reviewed (detected under mutex in setupReviewEnvironment).
	// This check is now safe from race conditions because it was performed while holding the repo mutex.
	if reviewEnv.skipReview {
		// Mark check run as completed so the PR status doesn't stay pending
		if err := reviewEnv.statusUpdater.Completed(ctx, event, reviewEnv.checkRunID,
			"success", "Review Already Exists", "This commit was already reviewed."); err != nil {
			j.logger.Warn("failed to mark check run as completed for skipped review",
				"error", err, "repo", event.RepoFullName, "pr", event.PRNumber)
		}
		return nil
	}
	if reviewEnv.pendingReview != nil {
		parsed, parseErr := agentreview.NewStructuredReviewParser(j.logger).Parse(ctx, reviewEnv.pendingReview.ReviewContent)
		if parseErr != nil {
			return fmt.Errorf("failed to parse pending review %d for retry: %w", reviewEnv.pendingReview.ID, parseErr)
		}
		return j.completeReview(ctx, event, reviewEnv, parsed, nil)
	}

	structuredReview, validFiles, err := j.processRepository(ctx, event, reviewEnv)
	if err != nil {
		return err
	}

	return j.completeReview(ctx, event, reviewEnv, structuredReview, validFiles)
}

type reviewEnvironment struct {
	ghClient      github.Client
	repo          *storage.Repository
	statusUpdater github.StatusUpdater
	checkRunID    int64
	updateResult  *core.UpdateResult
	repoConfig    *core.RepoConfig
	skipReview    bool // Set to true if review should be skipped (duplicate SHA)
	pendingReview *core.Review
}

// setupReviewEnvironment initializes clients, syncs the repo to the default branch,
// and loads all necessary configs. The repo mutex is held only for this phase to
// prevent concurrent git operations on the same repo. It is released before any
// LLM call so multiple PRs can generate reviews concurrently.
func (j *ReviewJob) setupReviewEnvironment(ctx context.Context, event *core.GitHubEvent, title, summary string) (*reviewEnvironment, error) {
	ghClient, ghToken, statusUpdater, checkRunID, err := j.setupReview(ctx, event, title, summary)
	if err != nil {
		return nil, err
	}

	// ── Mutex: protect only the Git sync phase ─────────────────────────────
	// The lock is acquired here and released at the end of this function.
	// The review (LLM call) runs completely outside the lock.
	mutex := j.getRepoMutex(event.RepoFullName)
	mutex.Lock()

	updateResult, syncErr := j.repoMgr.SyncRepo(ctx, event, ghToken)
	if syncErr != nil {
		mutex.Unlock() // release before error return
		syncErr = fmt.Errorf("failed to sync repository: %w", syncErr)
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, syncErr)
		return nil, syncErr
	}

	repo, repoErr := j.repoMgr.GetRepoRecord(ctx, event.RepoFullName)
	if repoErr != nil || repo == nil {
		mutex.Unlock()
		repoErr = fmt.Errorf("failed to retrieve repository record after sync for %s: %w", event.RepoFullName, repoErr)
		j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, repoErr)
		return nil, repoErr
	}

	// Check for duplicate review WHILE HOLDING THE LOCK ───────────────────
	// This prevents a race condition where two concurrent webhooks for the same PR
	// could both pass the SHA check and generate duplicate reviews.
	skipReview := false
	var pendingReview *core.Review
	existing, err := j.store.GetLatestReviewForPR(ctx, event.RepoFullName, event.PRNumber)
	switch {
	case err != nil:
		j.logger.Warn("failed to check for existing review", "error", err, "repo", event.RepoFullName, "pr", event.PRNumber)
		// Continue with review on error - don't block reviews if DB check fails
	case existing == nil || existing.HeadSHA != event.HeadSHA:
		// No review exists for the current commit.
	case existing.PublicationStatus == storage.ReviewPublicationPublished:
		j.logger.Info("Skipping review — same SHA already reviewed (detected under mutex)",
			"repo", event.RepoFullName, "pr", event.PRNumber, "sha", event.HeadSHA)
		skipReview = true
	default:
		j.logger.Info("Retrying review publication — persisted review is still pending",
			"repo", event.RepoFullName, "pr", event.PRNumber, "sha", event.HeadSHA)
		pendingReview = existing
	}

	// ── Release lock before any LLM call ─────────────────────────────────────
	mutex.Unlock()

	repoConfig := j.loadAndProcessRepoConfig(updateResult.RepoPath, event.RepoFullName)

	return &reviewEnvironment{
		ghClient:      ghClient,
		repo:          repo,
		statusUpdater: statusUpdater,
		checkRunID:    checkRunID,
		updateResult:  updateResult,
		repoConfig:    repoConfig,
		skipReview:    skipReview,
		pendingReview: pendingReview,
	}, nil
}

// processRepository fetches the PR diff and changed files from GitHub, validates them,
// and runs the agent-based review.
func (j *ReviewJob) processRepository(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment) (*core.StructuredReview, map[string]map[int]struct{}, error) {
	// Fetch diff and changed files once — used for both validation and review generation
	diff, err := env.ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get PR diff: %w", err)
	}

	changedFiles, err := env.ghClient.GetChangedFiles(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get changed files for validation: %w", err)
	}

	if commits, cErr := env.ghClient.GetPullRequestCommits(ctx, event.RepoOwner, event.RepoName, event.PRNumber); cErr == nil {
		event.CommitMessages = commits
	} else {
		j.logger.Warn("failed to fetch commit messages, review will proceed without them", "error", cErr)
	}

	validLineMaps := github.BuildValidLineMap(changedFiles)

	// Agent-based review is the default engine. RAG retrieval is no longer used
	// for /review — the agent investigates the diff with grep + read_file.
	structuredReview, err := j.runAgentReview(ctx, event, diff, changedFiles)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate review: %w", err)
	}

	return structuredReview, validLineMaps, nil
}

// runAgentReview runs the agent-based multi-angle review.
func (j *ReviewJob) runAgentReview(ctx context.Context, event *core.GitHubEvent, diff string, changedFiles []github.ChangedFile) (*core.StructuredReview, error) {
	repoURL := j.buildAgentCloneURL(event)

	executor := agentreview.NewGoframeAngleExecutor(j.llm, j.promptMgr, reviewtools.New, j.logger)
	runner := agentreview.NewRunner(executor, j.logger, nil)
	result, err := runner.Run(ctx, agentreview.Params{
		Diff:           diff,
		ChangedFiles:   changedFiles,
		RepoURL:        repoURL,
		RepoFullName:   event.RepoFullName,
		CommitMessages: event.CommitMessages,
	})
	if err != nil {
		return nil, err
	}
	return result.Review, nil
}

// completeReview reserves the review in the database, publishes it to GitHub,
// records successful delivery, and completes the check run.
func (j *ReviewJob) completeReview(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment, structuredReview *core.StructuredReview, validLineMaps map[string]map[int]struct{}) error {
	rawReview, err := j.prepareReviewForPublication(structuredReview, validLineMaps)
	if err != nil {
		return err
	}

	// Serialize publication attempts for the same commit in this process. The
	// database unique constraint remains the cross-process reservation.
	publicationMutex := j.getRepoMutex(event.RepoFullName)
	publicationMutex.Lock()
	defer publicationMutex.Unlock()

	dbReview, published, err := j.reserveReviewPublication(ctx, event, rawReview)
	if err != nil {
		return err
	}
	if published {
		j.logger.Info("Review already published by concurrent webhook, skipping duplicate post",
			"repo", event.RepoFullName, "pr", event.PRNumber, "sha", event.HeadSHA)
		return j.completeReviewCheck(ctx, event, env)
	}

	// A failed post leaves the record pending so a later webhook can retry it.
	if err := env.statusUpdater.PostStructuredReview(ctx, event, structuredReview); err != nil {
		return fmt.Errorf("failed to post review comment to GitHub: %w", err)
	}

	if err := j.store.UpdateReviewPublicationStatus(ctx, dbReview.ID, storage.ReviewPublicationPublished); err != nil {
		return fmt.Errorf("review was posted to GitHub but publication status could not be recorded: %w", err)
	}

	if err := j.completeReviewCheck(ctx, event, env); err != nil {
		return err
	}

	j.logger.Info("Full review job completed successfully")
	return nil
}

func (j *ReviewJob) prepareReviewForPublication(structuredReview *core.StructuredReview, validLineMaps map[string]map[int]struct{}) (string, error) {
	structuredReview.Suggestions = FilterNonCodeSuggestions(j.logger, structuredReview.Suggestions)

	inlineSuggestions, offDiffSuggestions := ValidateSuggestionsByLine(j.logger, structuredReview.Suggestions, validLineMaps)
	structuredReview.Suggestions = inlineSuggestions

	if len(offDiffSuggestions) > 0 {
		structuredReview.Summary = appendOffDiffSuggestions(structuredReview.Summary, offDiffSuggestions)
	}

	rawReview, err := agentreview.MarshalStructuredReview(structuredReview)
	if err != nil {
		return "", fmt.Errorf("failed to serialize review: %w", err)
	}
	return rawReview, nil
}

// reserveReviewPublication reserves a delivery. The unique constraint prevents two
// workers from independently creating publication records for the same SHA.
func (j *ReviewJob) reserveReviewPublication(ctx context.Context, event *core.GitHubEvent, rawReview string) (*core.Review, bool, error) {
	dbReview := &core.Review{
		RepoFullName:      event.RepoFullName,
		PRNumber:          event.PRNumber,
		HeadSHA:           event.HeadSHA,
		ReviewContent:     rawReview,
		PublicationStatus: storage.ReviewPublicationPending,
	}
	err := j.store.SaveReview(ctx, dbReview)
	if err == nil {
		return dbReview, false, nil
	}
	if !errors.Is(err, storage.ErrDuplicateReview) {
		j.logger.Error("failed to save review to database", "error", err)
		return nil, false, fmt.Errorf("failed to save review record to database: %w", err)
	}

	existing, err := j.store.GetLatestReviewForPR(ctx, event.RepoFullName, event.PRNumber)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load duplicate review record: %w", err)
	}
	if existing.HeadSHA != event.HeadSHA {
		return nil, false, fmt.Errorf("duplicate review record has unexpected SHA %s, want %s", existing.HeadSHA, event.HeadSHA)
	}
	if existing.PublicationStatus != storage.ReviewPublicationPublished {
		j.logger.Info("Retrying pending GitHub review publication",
			"review_id", existing.ID, "repo", event.RepoFullName, "pr", event.PRNumber)
	}
	return existing, existing.PublicationStatus == storage.ReviewPublicationPublished, nil
}

func (j *ReviewJob) completeReviewCheck(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment) error {
	if err := env.statusUpdater.Completed(ctx, event, env.checkRunID, "success", "Review Complete", "AI analysis finished."); err != nil {
		return fmt.Errorf("failed to update completion status on GitHub: %w", err)
	}
	return nil
}

// appendOffDiffSuggestions adds off-diff suggestions to the summary in a collapsible section.
func appendOffDiffSuggestions(summary string, suggestions []core.Suggestion) string {
	var sb strings.Builder
	sb.WriteString(summary)
	sb.WriteString("\n\n<details>\n")
	fmt.Fprintf(&sb, "<summary>📝 %d off-diff observation(s)</summary>\n\n", len(suggestions))

	for _, s := range suggestions {
		// Extract a brief title from the first line of the comment
		briefTitle := extractBriefTitle(s.Comment)
		emoji := github.SeverityEmoji(s.Severity)
		alert := github.SeverityAlert(s.Severity)
		fmt.Fprintf(&sb, "- **%s:%d** %s %s [%s]: %s\n", s.FilePath, s.LineNumber, emoji, s.Severity, alert, briefTitle)
	}

	sb.WriteString("\n</details>")
	return sb.String()
}

func extractBriefTitle(comment string) string {
	lines := strings.Split(comment, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip known section markers precisely
		if strings.HasPrefix(trimmed, "Observation:") ||
			strings.HasPrefix(trimmed, "**Observation:**") ||
			strings.HasPrefix(trimmed, "Rationale:") ||
			strings.HasPrefix(trimmed, "**Rationale:") ||
			strings.HasPrefix(trimmed, "Fix:") ||
			strings.HasPrefix(trimmed, "**Fix:") ||
			strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, ">") {
			continue
		}
		return truncateTitle(trimmed, 80)
	}
	return "Issue identified"
}

// truncateTitle truncates a title to a maximum length.
func truncateTitle(title string, maxLen int) string {
	return stringsutil.Truncate(title, maxLen, "...")
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

	statusUpdater := github.NewStatusUpdater(ghClient, j.logger, j.cfg.AI.EnableCodeSuggestions)
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
	if event.InstallationID <= 0 {
		return fmt.Errorf("installation ID must be positive, got: %d", event.InstallationID)
	}

	if event.PRNumber <= 0 {
		return fmt.Errorf("pull request number must be positive for review, got: %d", event.PRNumber)
	}

	return nil
}

func (j *ReviewJob) loadAndProcessRepoConfig(repoPath, repoFullName string) *core.RepoConfig {
	return config.LoadRepoConfigWithDefaults(repoPath, repoFullName, j.logger)
}

// buildAgentCloneURL constructs a clone URL for the agent review workspace,
// embedding the installation token so the pure-Go Cloner can authenticate.
func (j *ReviewJob) buildAgentCloneURL(event *core.GitHubEvent) string {
	base := event.RepoCloneURL
	if base == "" {
		base = fmt.Sprintf("https://github.com/%s/%s.git", event.RepoOwner, event.RepoName)
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		return base
	}

	// https://github.com/owner/repo.git -> https://x-access-token:TOKEN@github.com/owner/repo.git
	if strings.HasPrefix(base, "https://") {
		return "https://x-access-token:" + token + "@" + strings.TrimPrefix(base, "https://")
	}
	return base
}
