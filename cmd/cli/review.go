package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/code-warden/internal/wire"
)

var reviewCmd = &cobra.Command{
	Use:   "review [pr-url]",
	Short: "Run a RAG-based code review for a GitHub Pull Request",
	Args:  cobra.ExactArgs(1),
	RunE:  runReview,
}

func init() { //nolint:gochecknoinits // Cobra command registration
	rootCmd.AddCommand(reviewCmd)
}

func runReview(_ *cobra.Command, args []string) error {
	ctx := context.Background()
	prURL := args[0]
	fmt.Printf("Initializng Code Warden for PR: %s\n", prURL)

	// 1. Initialize Application
	app, cleanup, err := wire.InitializeApp(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize app: %w", err)
	}
	defer cleanup()

	// 1.1 Run Migrations
	fmt.Println("Applying database migrations...")
	if err := app.DB.RunMigrations(); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// 2. Parse URL
	owner, repoName, prNumber, err := gitutil.ParsePullRequestURL(prURL)
	if err != nil {
		return err
	}

	// 3. Init GitHub Client (PAT)
	if app.Cfg.GitHub.Token == "" {
		return fmt.Errorf("GITHUB_TOKEN is not set in configuration")
	}
	ghClient := github.NewPATClient(ctx, app.Cfg.GitHub.Token, app.Logger)

	// 4. Fetch PR Metadata
	fmt.Println("Fetching PR metadata...")
	pr, err := ghClient.GetPullRequest(ctx, owner, repoName, prNumber)
	if err != nil {
		return fmt.Errorf("failed to fetch PR: %w", err)
	}

	fmt.Printf("PR found: #%d %s\n", pr.GetNumber(), pr.GetTitle())
	fmt.Printf("Head SHA: %s\n", pr.GetHead().GetSHA())
	fmt.Printf("Language: %s\n", pr.GetBase().GetRepo().GetLanguage())

	event := &core.GitHubEvent{
		Type:         core.FullReview,
		RepoOwner:    owner,
		RepoName:     repoName,
		RepoFullName: fmt.Sprintf("%s/%s", owner, repoName),
		PRNumber:     prNumber,
		PRTitle:      pr.GetTitle(),
		PRBody:       pr.GetBody(),
		RepoCloneURL: pr.GetBase().GetRepo().GetCloneURL(),

		HeadSHA:  pr.GetHead().GetSHA(),
		Language: pr.GetBase().GetRepo().GetLanguage(),
	}

	// 5. Sync Repository
	fmt.Println("Syncing repository...")
	syncResult, err := app.RepoMgr.SyncRepo(ctx, event, app.Cfg.GitHub.Token)
	if err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}
	fmt.Printf("Repo synced to: %s\n", syncResult.RepoPath)
	if len(syncResult.FilesToAddOrUpdate) > 0 {
		fmt.Printf("Files modified in PR: %d\n", len(syncResult.FilesToAddOrUpdate))
	} else {
		fmt.Println("No modified files detected needing index update (or full re-index).")
	}

	// 5.1 Fetch Repo Record to get Config/Collection Names
	repo, err := app.RepoMgr.GetRepoRecord(ctx, event.RepoFullName)
	if err != nil {
		return fmt.Errorf("failed to get repo record: %w", err)
	}
	if repo == nil {
		return fmt.Errorf("repository record not found after sync")
	}

	// 6. Indexing (Setup or Update)
	repoPath := syncResult.RepoPath
	collectionName := repo.QdrantCollectionName
	embedderModel := repo.EmbedderModelName

	if err := handleIndexing(ctx, app, syncResult, repo, repoPath, collectionName, embedderModel); err != nil {
		return err
	}

	// 7. Generate Review
	return generateAndPrintReview(ctx, app, repo, event, ghClient)
}

func handleIndexing(ctx context.Context, a *app.App, syncResult *core.UpdateResult, repo *storage.Repository, repoPath, collectionName, embedderModel string) error {
	switch {
	case syncResult.IsInitialClone:
		fmt.Printf("Performing initial full indexing (Collection: %s, Model: %s)...\n", collectionName, embedderModel)
		if err := a.RAGService.SetupRepoContext(ctx, nil, collectionName, embedderModel, repoPath); err != nil {
			return fmt.Errorf("failed to setup repo context: %w", err)
		}
		fmt.Println("Repository context setup complete.")
	case len(syncResult.FilesToAddOrUpdate) > 0 || len(syncResult.FilesToDelete) > 0:
		fmt.Printf("Updating repository context (Collection: %s, Model: %s)...\n", collectionName, embedderModel)
		fmt.Printf("Processing %d modified/added files and %d deleted files...\n", len(syncResult.FilesToAddOrUpdate), len(syncResult.FilesToDelete))
		if err := a.RAGService.UpdateRepoContext(ctx, nil, repo, repoPath, syncResult.FilesToAddOrUpdate, syncResult.FilesToDelete); err != nil {
			return fmt.Errorf("failed to update repo context: %w", err)
		}
		fmt.Println("Repository context update complete.")
	default:
		fmt.Println("Repository context is up to date. Skipping indexing.")
	}
	return nil
}

func generateAndPrintReview(ctx context.Context, a *app.App, repo *storage.Repository, event *core.GitHubEvent, ghClient github.Client) error {
	fmt.Println("Generating review (this may take a few seconds)...")
	review, _, err := a.RAGService.GenerateReview(ctx, nil, repo, event, ghClient)
	if err != nil {
		return fmt.Errorf("failed to generate review: %w", err)
	}
	fmt.Println("Review generation complete.")
	printReview(review)
	return nil
}

func printReview(review *core.StructuredReview) {
	fmt.Println("\n================ REVIEW SUMMARY ================")
	fmt.Println(review.Summary)
	fmt.Println("\n================ SUGGESTIONS ================")
	if len(review.Suggestions) == 0 {
		fmt.Println("No suggestions.")
		return
	}
	for _, s := range review.Suggestions {
		fmt.Printf("[%s] %s:%d\n", s.Severity, s.FilePath, s.LineNumber)
		fmt.Printf("%s\n\n", s.Comment)
	}
}
