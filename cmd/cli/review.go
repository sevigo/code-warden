package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/code-warden/internal/wire"
)

var verbose bool

// Color definitions for terminal output.
var (
	titleColor   = color.New(color.FgCyan, color.Bold)
	successColor = color.New(color.FgGreen)
	warnColor    = color.New(color.FgYellow)
	infoColor    = color.New(color.FgWhite)
	dimColor     = color.New(color.FgHiBlack)
	boldColor    = color.New(color.Bold)
)

var reviewCmd = &cobra.Command{
	Use:   "review [pr-url]",
	Short: "Run a RAG-based code review for a GitHub Pull Request",
	Long: `Run a RAG-based code review for a GitHub Pull Request.

The review command fetches the PR diff, builds context from the repository's
vector store, and uses an LLM to generate a structured code review.

Examples:
  warden-cli review https://github.com/owner/repo/pull/123
  warden-cli review --verbose https://github.com/owner/repo/pull/123`,
	Args: cobra.ExactArgs(1),
	RunE: runReview,
}

func init() { //nolint:gochecknoinits // Cobra command registration
	reviewCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output with timing information")
	rootCmd.AddCommand(reviewCmd)
}

// stepTimer tracks timing for verbose output.
type stepTimer struct {
	stepNum    int
	totalSteps int
	start      time.Time
	verbose    bool
}

func newStepTimer(totalSteps int, verboseMode bool) *stepTimer {
	return &stepTimer{
		stepNum:    0,
		totalSteps: totalSteps,
		verbose:    verboseMode,
	}
}

func (t *stepTimer) step(name string) {
	t.stepNum++
	t.start = time.Now()
	if t.verbose {
		//nolint:gosec // CLI output, errors are intentionally ignored
		titleColor.Printf("\n🔧 Step %d/%d: %s...\n", t.stepNum, t.totalSteps, name)
	} else {
		fmt.Printf("%s...\n", name)
	}
}

func (t *stepTimer) done() {
	if t.verbose {
		elapsed := time.Since(t.start).Round(time.Millisecond)
		//nolint:gosec // CLI output, errors are intentionally ignored
		successColor.Printf("   ✓ Done (%s)\n", elapsed)
	}
}

func (t *stepTimer) infof(format string, args ...any) {
	if t.verbose {
		//nolint:gosec // CLI output, errors are intentionally ignored
		dimColor.Printf("   ├── "+format+"\n", args...)
	}
}

func runReview(_ *cobra.Command, args []string) error {
	ctx := context.Background()
	prURL := args[0]

	timer := newStepTimer(5, verbose)
	overallStart := time.Now()

	printHeader(prURL)

	// 1. Initialize Application
	timer.step("Initializing application")
	appInstance, cleanup, err := wire.InitializeApp(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize app: %w\n\nTip: Check that your config.yaml exists and is valid", err)
	}
	defer cleanup()

	if err := appInstance.DB.RunMigrations(); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	timer.done()

	// 2. Parse URL and fetch PR metadata
	timer.step("Fetching PR metadata")
	event, ghClient, err := fetchPRMetadata(ctx, appInstance, prURL, timer)
	if err != nil {
		return err
	}
	timer.done()

	// 3. Sync Repository
	timer.step("Syncing repository")
	syncResult, repo, err := syncRepository(ctx, appInstance, event, timer)
	if err != nil {
		return err
	}
	timer.done()

	// 4. Indexing
	timer.step("Updating index")
	if err := handleIndexing(ctx, appInstance, syncResult, repo, timer); err != nil {
		return err
	}
	// Save the indexed SHA before the LLM call so we don't lose indexing progress if review fails
	if event.HeadSHA != "" {
		if err := appInstance.RepoMgr.UpdateRepoSHA(ctx, event.RepoFullName, event.HeadSHA); err != nil {
			return fmt.Errorf("failed to update repo SHA: %w", err)
		}
	}
	timer.done()

	// 5. Generate Review
	timer.step("Generating review")

	var review *core.StructuredReview
	if len(appInstance.Cfg.AI.ComparisonModels) > 0 {
		if len(appInstance.Cfg.AI.ComparisonModels) < 2 {
			return fmt.Errorf("consensus review requires at least 2 models, got %d", len(appInstance.Cfg.AI.ComparisonModels))
		}
		// check for duplicates
		seen := make(map[string]bool)
		for _, m := range appInstance.Cfg.AI.ComparisonModels {
			if seen[m] {
				return fmt.Errorf("duplicate model in comparison_models: %s", m)
			}
			seen[m] = true
		}

		timer.infof("Running consensus review with %d models...", len(appInstance.Cfg.AI.ComparisonModels))
		// In consensus mode, we get a single synthesized review
		review, err = appInstance.RAGService.GenerateConsensusReview(ctx, nil, repo, event, ghClient, appInstance.Cfg.AI.ComparisonModels)
		if err != nil {
			return fmt.Errorf("consensus review failed: %w", err)
		}
	} else {
		// Standard single-model review
		review, err = generateReview(ctx, appInstance, repo, event, ghClient, timer)
		if err != nil {
			return err
		}
	}
	timer.done()

	// Print results
	if verbose {
		//nolint:gosec // CLI output
		dimColor.Printf("\n⏱️  Total time: %s\n", time.Since(overallStart).Round(time.Millisecond))
	}

	printReview(review)
	return nil
}

func printHeader(prURL string) {
	//nolint:gosec // CLI output, errors are intentionally ignored
	titleColor.Println("🚀 Code Warden - PR Review")
	//nolint:gosec // CLI output
	dimColor.Printf("   Target: %s\n\n", prURL)
}

func fetchPRMetadata(ctx context.Context, appInstance *app.App, prURL string, timer *stepTimer) (*core.GitHubEvent, github.Client, error) {
	owner, repoName, prNumber, err := gitutil.ParsePullRequestURL(prURL)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid PR URL: %w\n\nExpected format: https://github.com/owner/repo/pull/123", err)
	}

	if appInstance.Cfg.GitHub.Token == "" {
		return nil, nil, fmt.Errorf("GITHUB_TOKEN is not set\n\nTip: Set CW_GITHUB_TOKEN or GITHUB_TOKEN environment variable")
	}
	ghClient := github.NewPATClient(ctx, appInstance.Cfg.GitHub.Token, appInstance.Logger)

	pr, err := ghClient.GetPullRequest(ctx, owner, repoName, prNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch PR: %w\n\nTip: Check that the PR exists and your token has access", err)
	}

	timer.infof("PR #%d: %s", pr.GetNumber(), pr.GetTitle())
	timer.infof("Head SHA: %s", truncateSHA(pr.GetHead().GetSHA()))
	timer.infof("Language: %s", pr.GetBase().GetRepo().GetLanguage())

	event := &core.GitHubEvent{
		Type:         core.FullReview,
		RepoOwner:    owner,
		RepoName:     repoName,
		RepoFullName: fmt.Sprintf("%s/%s", owner, repoName),
		PRNumber:     prNumber,
		PRTitle:      pr.GetTitle(),
		PRBody:       pr.GetBody(),
		RepoCloneURL: pr.GetBase().GetRepo().GetCloneURL(),
		HeadSHA:      pr.GetHead().GetSHA(),
		Language:     pr.GetBase().GetRepo().GetLanguage(),
	}

	return event, ghClient, nil
}

func syncRepository(ctx context.Context, appInstance *app.App, event *core.GitHubEvent, timer *stepTimer) (*core.UpdateResult, *storage.Repository, error) {
	syncResult, err := appInstance.RepoMgr.SyncRepo(ctx, event, appInstance.Cfg.GitHub.Token)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sync repo: %w\n\nTip: Check network connectivity and disk space", err)
	}
	timer.infof("Path: %s", syncResult.RepoPath)
	if syncResult.IsInitialClone {
		timer.infof("Initial clone completed")
	} else if len(syncResult.FilesToAddOrUpdate) > 0 {
		timer.infof("Files changed: %d", len(syncResult.FilesToAddOrUpdate))
	}

	repo, err := appInstance.RepoMgr.GetRepoRecord(ctx, event.RepoFullName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get repo record: %w", err)
	}
	if repo == nil {
		return nil, nil, fmt.Errorf("repository record not found after sync")
	}

	return syncResult, repo, nil
}

func generateReview(ctx context.Context, appInstance *app.App, repo *storage.Repository, event *core.GitHubEvent, ghClient github.Client, timer *stepTimer) (*core.StructuredReview, error) {
	review, _, err := appInstance.RAGService.GenerateReview(ctx, nil, repo, event, ghClient)
	if err != nil {
		return nil, fmt.Errorf("failed to generate review: %w\n\nTip: Check that the LLM service is running", err)
	}
	timer.infof("Suggestions: %d", len(review.Suggestions))
	return review, nil
}

func handleIndexing(ctx context.Context, a *app.App, syncResult *core.UpdateResult, repo *storage.Repository, timer *stepTimer) error {
	repoPath := syncResult.RepoPath
	collectionName := repo.QdrantCollectionName

	switch {
	case syncResult.IsInitialClone:
		timer.infof("Performing initial full indexing")
		timer.infof("Collection: %s", collectionName)
		if err := a.RAGService.SetupRepoContext(ctx, nil, repo, repoPath); err != nil {
			return fmt.Errorf("failed to setup repo context: %w", err)
		}
	case len(syncResult.FilesToAddOrUpdate) > 0 || len(syncResult.FilesToDelete) > 0:
		timer.infof("Incremental update: %d added/modified, %d deleted",
			len(syncResult.FilesToAddOrUpdate), len(syncResult.FilesToDelete))
		if err := a.RAGService.UpdateRepoContext(ctx, nil, repo, repoPath, syncResult.FilesToAddOrUpdate, syncResult.FilesToDelete); err != nil {
			return fmt.Errorf("failed to update repo context: %w", err)
		}
	default:
		timer.infof("Index up to date, skipping")
	}
	return nil
}

func truncateSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func printReview(review *core.StructuredReview) {
	separator := strings.Repeat("═", 60)
	thinSeparator := strings.Repeat("─", 60)

	fmt.Println()
	//nolint:gosec // CLI output, errors are intentionally ignored
	titleColor.Println(separator)
	//nolint:gosec // CLI output
	titleColor.Println("📋 REVIEW SUMMARY")
	//nolint:gosec // CLI output
	titleColor.Println(separator)
	fmt.Println()
	//nolint:gosec // CLI output
	infoColor.Println(review.Summary)

	if len(review.Suggestions) == 0 {
		fmt.Println()
		//nolint:gosec // CLI output
		successColor.Println("✅ No issues found!")
		return
	}

	fmt.Println()
	//nolint:gosec // CLI output
	warnColor.Println(thinSeparator)
	//nolint:gosec // CLI output
	warnColor.Printf("💡 SUGGESTIONS (%d)\n", len(review.Suggestions))
	//nolint:gosec // CLI output
	warnColor.Println(thinSeparator)

	for i, s := range review.Suggestions {
		fmt.Println()
		printSeverityBadge(s.Severity)
		//nolint:gosec // CLI output
		boldColor.Printf(" %s", s.FilePath)
		//nolint:gosec // CLI output
		dimColor.Printf(":%d\n", s.LineNumber)

		if s.Category != "" {
			//nolint:gosec // CLI output
			dimColor.Printf("   Category: %s\n", s.Category)
		}
		fmt.Println()
		//nolint:gosec // CLI output
		infoColor.Printf("%s\n", s.Comment)

		if i < len(review.Suggestions)-1 {
			fmt.Println()
			//nolint:gosec // CLI output
			dimColor.Println(strings.Repeat("─", 40))
		}
	}
	fmt.Println()
}

func printSeverityBadge(severity string) {
	//nolint:gosec // CLI output, errors are intentionally ignored
	switch severity {
	case "Critical":
		color.New(color.BgRed, color.FgWhite, color.Bold).Printf(" %s ", severity)
	case "High":
		color.New(color.BgHiRed, color.FgWhite).Printf(" %s ", severity)
	case "Medium":
		color.New(color.BgYellow, color.FgBlack).Printf(" %s ", severity)
	case "Low":
		color.New(color.BgGreen, color.FgWhite).Printf(" %s ", severity)
	default:
		color.New(color.BgWhite, color.FgBlack).Printf(" %s ", severity)
	}
}
