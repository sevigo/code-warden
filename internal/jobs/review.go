package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/code-warden/internal/agent"
	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/globalmcp"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/repomanager"
	reviewpkg "github.com/sevigo/code-warden/internal/review"
	"github.com/sevigo/code-warden/internal/storage"
)

var (
	ErrConfigNotFound = errors.New("config file not found")
	ErrConfigParsing  = errors.New("config parsing failed")
)

type ReviewJob struct {
	cfg               *config.Config
	ragService        rag.Service
	store             storage.Store
	vectorStore       storage.VectorStore
	repoMgr           repomanager.RepoManager
	logger            *slog.Logger
	globalMCPRegistry *globalmcp.WorkspaceRegistry
	repoMutexes       sync.Map
}

// NewReviewJob creates a new ReviewJob.
func NewReviewJob(
	cfg *config.Config,
	rag rag.Service,
	store storage.Store,
	vectorStore storage.VectorStore,
	repoMgr repomanager.RepoManager,
	logger *slog.Logger,
	globalMCPRegistry *globalmcp.WorkspaceRegistry,
) core.Job {
	return &ReviewJob{
		cfg:               cfg,
		ragService:        rag,
		store:             store,
		vectorStore:       vectorStore,
		repoMgr:           repoMgr,
		logger:            logger,
		globalMCPRegistry: globalMCPRegistry,
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

// Run acts as a router, directing the event to the correct review flow.
func (j *ReviewJob) Run(ctx context.Context, event *core.GitHubEvent) error {
	// Log the command type
	j.logger.Info("processing GitHub event",
		"type", event.Type,
		"repo", event.RepoFullName,
		"pr", event.PRNumber,
		"issue", event.IssueNumber,
		"commenter", event.Commenter)

	if err := j.validateInputs(event); err != nil {
		j.logger.Error("Input validation failed", "error", err)
		return err
	}

	switch event.Type {
	case core.FullReview:
		return j.runFullReview(ctx, event)
	case core.ReReview:
		return j.runReReview(ctx, event)
	case core.ImplementIssue:
		return j.runImplementIssue(ctx, event)
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

// runImplementIssue handles the `/implement` command on issues.
func (j *ReviewJob) runImplementIssue(ctx context.Context, event *core.GitHubEvent) error {
	j.logger.Info("🤖 Starting Issue Implementation",
		"repo", event.RepoFullName,
		"issue", event.IssueNumber,
		"title", event.IssueTitle)

	// Check if agent is enabled
	if !j.cfg.Agent.Enabled {
		j.logger.Warn("agent functionality is disabled")
		return fmt.Errorf("agent functionality is disabled; enable it in config to use /implement")
	}

	// 1. Create GitHub client for the installation
	ghClient, ghToken, err := github.CreateInstallationClient(ctx, j.cfg, event.InstallationID, j.logger)
	if err != nil {
		return fmt.Errorf("failed to create GitHub client: %w", err)
	}

	// 2. Sync the repository to get the latest code
	updateResult, err := j.repoMgr.SyncRepo(ctx, event, "")
	if err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	// 3. Get the repository record
	repo, err := j.repoMgr.GetRepoRecord(ctx, event.RepoFullName)
	if err != nil {
		return fmt.Errorf("failed to get repo record: %w", err)
	}

	// 4. Load repository config
	repoConfig := j.loadAndProcessRepoConfig(updateResult.RepoPath, event.RepoFullName)

	// 5. Get scoped vector store for this repo
	scopedStore := j.vectorStore.ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName)

	// 6. Parse agent timeout
	timeout, err := j.cfg.Agent.GetTimeout()
	if err != nil {
		return fmt.Errorf("invalid agent timeout: %w", err)
	}

	// 7. Create orchestrator
	// For agent iterations, use a single randomly-selected comparison model (if configured)
	// instead of full consensus review. This provides better quality than the generator model
	// alone while keeping review time within the 60-second MCP tool timeout.
	// Full consensus review (3 models) takes 90-180+ seconds which causes client timeouts.
	var agentComparisonModel []string
	if len(j.cfg.AI.ComparisonModels) > 0 {
		// Randomly select one model from the comparison models
		//nolint:gosec // G404: Random selection of review model, not security-sensitive
		selectedModel := j.cfg.AI.ComparisonModels[rand.IntN(len(j.cfg.AI.ComparisonModels))]
		agentComparisonModel = []string{selectedModel}
		j.logger.Info("agent using single comparison model for faster review",
			"selected_model", selectedModel,
			"available_models", j.cfg.AI.ComparisonModels)
	}

	orchestrator := agent.NewOrchestrator(
		j.store,
		scopedStore,
		j.ragService,
		ghClient,
		ghToken,
		repo,
		repoConfig,
		updateResult.RepoPath,
		agent.Config{
			Enabled:               j.cfg.Agent.Enabled,
			Provider:              j.cfg.Agent.Provider,
			Model:                 j.cfg.Agent.Model,
			Timeout:               timeout,
			MaxIterations:         j.cfg.Agent.MaxIterations,
			MaxConcurrentSessions: j.cfg.Agent.MaxConcurrentSessions,
			MCPAddr:               j.cfg.Agent.MCPAddr,
			MCPTimeout:            j.cfg.Agent.GetMCPTimeout(),
			WorkingDir:            j.cfg.Agent.WorkingDir,
			ComparisonModels:      agentComparisonModel,
			ReviewsDir:            firstNonEmpty(j.cfg.AI.ReviewsDir, "reviews"),
		},
		j.logger,
		j.globalMCPRegistry,
	)

	// 8. Start the MCP server
	if err := orchestrator.Start(); err != nil {
		return fmt.Errorf("failed to start orchestrator: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := orchestrator.Shutdown(shutdownCtx); err != nil {
			j.logger.Error("failed to shutdown orchestrator", "error", err)
		}
	}()

	// 9. Spawn agent to implement the issue
	issue := agent.Issue{
		Number:       event.IssueNumber,
		Title:        event.IssueTitle,
		Body:         event.IssueBody,
		Instructions: event.UserInstructions,
		RepoOwner:    event.RepoOwner,
		RepoName:     event.RepoName,
	}

	session, err := orchestrator.SpawnAgent(ctx, issue)
	if err != nil {
		return fmt.Errorf("failed to spawn agent: %w", err)
	}

	// 10. Monitor session and wait for completion
	result, err := j.waitForAgentSession(ctx, orchestrator, session, timeout)
	if err != nil {
		return err
	}

	// 11. Post result as comment on the issue
	comment := j.formatImplementResult(result)
	return ghClient.CreateComment(ctx, event.RepoOwner, event.RepoName, event.IssueNumber, comment)
}

// waitForAgentSession monitors the agent session until completion or timeout.
func (j *ReviewJob) waitForAgentSession(ctx context.Context, orchestrator *agent.Orchestrator, session *agent.Session, timeout time.Duration) (*agent.Result, error) {
	j.logger.Info("agent session started, waiting for completion",
		"session_id", session.ID,
		"timeout", timeout)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		select {
		case <-timeoutCtx.Done():
			if err := orchestrator.CancelSession(session.ID); err != nil {
				j.logger.Error("failed to cancel session on timeout", "error", err)
			}
			return nil, fmt.Errorf("agent session timed out after %v", timeout)

		case <-ticker.C:
			snapshot := session.Snapshot()
			j.logger.Info("agent session status",
				"session_id", session.ID,
				"status", snapshot.Status,
				"duration", time.Since(snapshot.StartedAt).Round(time.Second))

			switch snapshot.Status {
			case agent.StatusCompleted:
				if snapshot.Result == nil {
					return nil, fmt.Errorf("agent completed with no result")
				}
				j.logger.Info("agent session completed",
					"session_id", session.ID,
					"pr_number", snapshot.Result.PRNumber,
					"pr_url", snapshot.Result.PRURL,
					"verdict", snapshot.Result.Verdict,
					"iterations", snapshot.Result.Iterations)
				return snapshot.Result, nil

			case agent.StatusFailed:
				return nil, fmt.Errorf("agent session failed: %s", snapshot.Error)

			case agent.StatusCancelled:
				return nil, fmt.Errorf("agent session was cancelled: %s", snapshot.Error)
			}
		}
	}
}

// formatImplementResult creates a comment body from the agent result.
func (j *ReviewJob) formatImplementResult(result *agent.Result) string {
	var sb strings.Builder

	if result.PRNumber > 0 {
		sb.WriteString("## ✅ Implementation Complete\n\n")
		sb.WriteString(fmt.Sprintf("I've created pull request [#%d](%s) with the implementation.\n\n", result.PRNumber, result.PRURL))
		sb.WriteString(fmt.Sprintf("**Branch:** `%s`\n", result.Branch))
		sb.WriteString(fmt.Sprintf("**Files Changed:** %d\n", len(result.FilesChanged)))
		sb.WriteString(fmt.Sprintf("**Iterations:** %d\n", result.Iterations))
		sb.WriteString(fmt.Sprintf("**Verdict:** %s\n", result.Verdict))

		if result.ReviewSummary != "" {
			sb.WriteString(fmt.Sprintf("\n**Review Summary:**\n%s\n", result.ReviewSummary))
		}
	} else {
		sb.WriteString("## ❌ Implementation Failed\n\n")
		sb.WriteString("The agent was unable to complete the implementation. Please check the logs for details.\n")
	}

	return sb.String()
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

	// 4. Post the result
	if err = reviewEnv.statusUpdater.PostStructuredReview(ctx, event, structuredReview); err != nil {
		return fmt.Errorf("failed to post re-review comment: %w", err)
	}

	// Update reReviewContent for DB save
	reReviewContent := structuredReview.Summary

	// 5. Save the re-review as a new review record? Yes, to maintain history.
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
	skipReview    bool // Set to true if review should be skipped (duplicate SHA)
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

	// ── Mutex: protect only the Git sync + optional Qdrant update phase ──────
	// The lock is acquired here and released at the end of this function.
	// GenerateReview (LLM call) runs completely outside the lock.
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

	// Update vector store only when the default branch has new commits.
	// PR diffs are NEVER written to Qdrant; they are passed in-memory to the LLM.
	if updateResult.IsInitialClone || updateResult.DefaultBranchChanged {
		if vsErr := j.updateVectorStoreAndSHA(ctx, j.loadAndProcessRepoConfig(updateResult.RepoPath, event.RepoFullName), repo, updateResult); vsErr != nil {
			mutex.Unlock()
			j.updateStatusOnError(ctx, statusUpdater, event, checkRunID, vsErr)
			return nil, vsErr
		}
	} else {
		j.logger.Info("default branch unchanged — skipping Qdrant update, running review off existing index",
			"repo", event.RepoFullName,
			"default_branch_sha", updateResult.DefaultBranchSHA,
		)
	}

	// ── Check for duplicate review WHILE HOLDING THE LOCK ───────────────────
	// This prevents a race condition where two concurrent webhooks for the same PR
	// could both pass the SHA check and generate duplicate reviews.
	skipReview := false
	if event.Type == core.FullReview {
		existing, err := j.store.GetLatestReviewForPR(ctx, event.RepoFullName, event.PRNumber)
		if err != nil {
			j.logger.Warn("failed to check for existing review", "error", err, "repo", event.RepoFullName, "pr", event.PRNumber)
			// Continue with review on error - don't block reviews if DB check fails
		} else if existing != nil && existing.HeadSHA == event.HeadSHA {
			j.logger.Info("Skipping review — same SHA already reviewed (detected under mutex)",
				"repo", event.RepoFullName, "pr", event.PRNumber, "sha", event.HeadSHA)
			skipReview = true
		}
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
	}, nil
}

// processRepository fetches the PR diff and changed files from GitHub, validates them,
// and runs the LLM-based review. The Qdrant index is NOT modified here.
func (j *ReviewJob) processRepository(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment) (*core.StructuredReview, string, map[string]map[int]struct{}, error) {
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
		lines, err := github.ParseValidLinesFromPatch(f.Patch, j.logger)
		if err != nil {
			j.logger.Error("failed to parse valid lines from patch", "file", f.Filename, "error", err)
			continue
		}
		validLineMaps[f.Filename] = lines
	}

	executor := reviewpkg.NewExecutor(j.ragService, reviewpkg.Config{
		ComparisonModels: j.cfg.AI.ComparisonModels,
		ReviewsDir:       j.cfg.AI.ReviewsDir,
		Logger:           j.logger,
	})

	result, err := executor.Execute(ctx, reviewpkg.Params{
		RepoConfig:   env.repoConfig,
		Repo:         env.repo,
		Event:        event,
		Diff:         diff,
		ChangedFiles: changedFiles,
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to generate review: %w", err)
	}

	return result.Review, result.RawReview, validLineMaps, nil
}

// completeReview posts the review to GitHub, saves it to the DB, and marks the check run as successful.
// It uses a database unique constraint to prevent duplicate reviews for the same SHA.
func (j *ReviewJob) completeReview(ctx context.Context, event *core.GitHubEvent, env *reviewEnvironment, structuredReview *core.StructuredReview, rawReview string, validLineMaps map[string]map[int]struct{}) error {
	// Filter out non-code file suggestions first
	structuredReview.Suggestions = FilterNonCodeSuggestions(j.logger, structuredReview.Suggestions)

	// Validate and filter suggestions to prevent 422 errors
	inlineSuggestions, offDiffSuggestions := ValidateSuggestionsByLine(j.logger, structuredReview.Suggestions, validLineMaps)
	structuredReview.Suggestions = inlineSuggestions

	// If there are off-diff suggestions, append them to the summary in a collapsible section
	if len(offDiffSuggestions) > 0 {
		structuredReview.Summary = appendOffDiffSuggestions(structuredReview.Summary, offDiffSuggestions)
	}

	// Save to DB first - the unique constraint (repo_full_name, pr_number, head_sha) prevents duplicates.
	// If another concurrent webhook already saved a review for this SHA, we get ErrDuplicateReview.
	dbReview := &core.Review{
		RepoFullName:  event.RepoFullName,
		PRNumber:      event.PRNumber,
		HeadSHA:       event.HeadSHA,
		ReviewContent: rawReview,
	}
	err := j.store.SaveReview(ctx, dbReview)
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateReview) {
			// Another concurrent webhook already completed this review.
			// We still need to mark the check run as complete, but skip posting duplicate comments.
			j.logger.Info("Review already saved by concurrent webhook, skipping duplicate post",
				"repo", event.RepoFullName, "pr", event.PRNumber, "sha", event.HeadSHA)
			if completeErr := env.statusUpdater.Completed(ctx, event, env.checkRunID, "success", "Review Complete", "AI analysis finished."); completeErr != nil {
				j.logger.Warn("failed to update completion status", "error", completeErr)
			}
			return nil
		}
		j.logger.Error("failed to save review to database", "error", err)
		return fmt.Errorf("failed to save review record to database: %w", err)
	}

	// Only post to GitHub after successful DB save (prevents duplicate comments)
	if err := env.statusUpdater.PostStructuredReview(ctx, event, structuredReview); err != nil {
		return fmt.Errorf("failed to post review comment to GitHub: %w", err)
	}

	if err := env.statusUpdater.Completed(ctx, event, env.checkRunID, "success", "Review Complete", "AI analysis finished."); err != nil {
		return fmt.Errorf("failed to update completion status on GitHub: %w", err)
	}

	j.logger.Info("Full review job completed successfully")
	return nil
}

// appendOffDiffSuggestions adds off-diff suggestions to the summary in a collapsible section.
func appendOffDiffSuggestions(summary string, suggestions []core.Suggestion) string {
	var sb strings.Builder
	sb.WriteString(summary)
	sb.WriteString("\n\n<details>\n")
	sb.WriteString(fmt.Sprintf("<summary>📝 %d off-diff observation(s)</summary>\n\n", len(suggestions)))

	for _, s := range suggestions {
		// Extract a brief title from the first line of the comment
		briefTitle := extractBriefTitle(s.Comment)
		emoji := github.SeverityEmoji(s.Severity)
		alert := github.SeverityAlert(s.Severity)
		sb.WriteString(fmt.Sprintf("- **%s:%d** %s %s [%s]: %s\n", s.FilePath, s.LineNumber, emoji, s.Severity, alert, briefTitle))
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
	if len(title) <= maxLen {
		return title
	}
	return title[:maxLen-3] + "..."
}

// updateVectorStoreAndSHA performs incremental indexing of the default branch changes.
// It persists DefaultBranchSHA (not the PR HeadSHA) as LastIndexedSHA to keep
// the Qdrant baseline aligned with main.
func (j *ReviewJob) updateVectorStoreAndSHA(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, updateResult *core.UpdateResult) error {
	switch {
	case updateResult.IsInitialClone:
		j.logger.Info("⚠️ Initial indexing required (fresh clone or reset state)", "repo", repo.FullName)
		err := j.ragService.SetupRepoContext(ctx, repoConfig, repo, updateResult.RepoPath)
		if err != nil {
			return fmt.Errorf("failed to perform initial repository indexing: %w", err)
		}

	case len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0:
		j.logger.Info("⚡ Incremental update required (default branch advanced)",
			"repo", repo.FullName,
			"changed_files", len(updateResult.FilesToAddOrUpdate),
			"deleted_files", len(updateResult.FilesToDelete),
		)
		err := j.ragService.UpdateRepoContext(ctx, repoConfig, repo, updateResult.RepoPath, updateResult.FilesToAddOrUpdate, updateResult.FilesToDelete)
		if err != nil {
			return fmt.Errorf("failed to update repository context in vector store: %w", err)
		}

	default:
		j.logger.Info("✅ Repository up-to-date. Skipping Scan.", "repo", repo.FullName)
	}

	// Persist the DEFAULT BRANCH SHA (not the PR HeadSHA) so the next sync
	// correctly computes the incremental diff against main.
	shaToStore := updateResult.DefaultBranchSHA
	if shaToStore == "" {
		// Defensive fallback — should not happen with the new sync logic
		shaToStore = updateResult.HeadSHA
		j.logger.Warn("DefaultBranchSHA was empty, falling back to HeadSHA for persistence",
			"repo", repo.FullName,
		)
	}

	if err := j.repoMgr.UpdateRepoSHA(ctx, repo.FullName, shaToStore); err != nil {
		j.logger.Error("CRITICAL: Vector store updated but failed to persist new SHA in database.",
			"error", err, "repo", repo.FullName, "new_sha", shaToStore)
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

	// Validate based on event type
	switch event.Type {
	case core.FullReview, core.ReReview:
		if event.PRNumber <= 0 {
			return fmt.Errorf("pull request number must be positive for review, got: %d", event.PRNumber)
		}
	case core.ImplementIssue:
		if event.IssueNumber <= 0 {
			return fmt.Errorf("issue number must be positive for implement, got: %d", event.IssueNumber)
		}
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

// firstNonEmpty returns the first non-empty string from the given strings.
// If all strings are empty, returns the empty string.
func firstNonEmpty(strings ...string) string {
	for _, s := range strings {
		if s != "" {
			return s
		}
	}
	return ""
}
