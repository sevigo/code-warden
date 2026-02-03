package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
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

	// For simplicity in CLI, we can assume we want to ensure it's updated.
	// Real webhook logic checks repo.Status or Clone vs Update.
	// We'll mimic the webhook handler's logic partially or just force an update context logic.
	// Since SyncRepo returns a repo object, let's check its output logic in real handler.
	// But CLI user likely wants latest.
	// SyncRepo handles the git pull. Now we need to update embeddings.
	// We'll just call SetupRepoContext if we want full or Update if we know what changed.
	// For CLI 'review', we likely want to ensure the vector store matches HEAD.
	// The most robust way for a "test" is to assume incremental update for the changed files in PR + ensure base is there.
	// However, without knowing previous commit, Update is hard.
	// SetupRepoContext does full index.
	// Let's rely on what the webhook does: It queues a job.
	// Here we want synchronous execution.

	// Check if we have embeddings. If new repo, Setup. If existing, maybe Update?
	// But UpdateRepoContext needs specific filesToProcess.
	// SetupRepoContext walks the whole repo.
	// Safest for "Review this PR" locally is:
	// If it was just cloned (brand new), run Setup.
	// If it existed, we might want to just run Setup (re-index) OR assume it's up to date except for PR changes?
	// Actually, RAG needs context from the whole repo.
	// Let's do a quick check: if collection exists?
	// app.VectorStore.CollectionExists? (Not exposed directly maybe).

	// Let's just run SetupRepoContext. It uses a loader with checks, but might be slow for huge repos.
	// But for "Testing logic", correct state is paramount.
	fmt.Printf("Ensuring repository context is indexed (Collection: %s, Model: %s)...\n", collectionName, embedderModel)
	if err := app.RAGService.SetupRepoContext(ctx, nil, collectionName, embedderModel, repoPath); err != nil {
		return fmt.Errorf("failed to setup repo context: %w", err)
	}
	fmt.Println("Repository context ready.")

	// 7. Generate Review
	fmt.Println("Generating review (this may take a few seconds)...")
	// We need a RepoConfig. Default or loaded from repo?
	// app.RepoMgr.GetConfig(repoPath)? (Not exposed).
	// Let's use nil (Default).
	review, _, err := app.RAGService.GenerateReview(ctx, nil, repo, event, ghClient)
	if err != nil {
		return fmt.Errorf("failed to generate review: %w", err)
	}
	fmt.Println("Review generation complete.")

	// 8. Output
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
