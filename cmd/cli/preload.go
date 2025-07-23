package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/wire"
)

var (repoURL string
	branch string
	githubToken string
)

var preloadCmd = &cobra.Command{
	Use:   "preload",
	Short: "Preload a repository into the vector store",
	Long:  `This command performs the initial clone and indexing of a repository for faster subsequent reviews.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		slog.Info("Initializing application...")
		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize app: %w", err)
		}
		defer cleanup()

		slog.Info("Fetching remote head SHA...", "url", repoURL, "branch", branch)
		gitClient := gitutil.NewClient(slog.Default()) // Assuming NewClient takes a logger
		sha, err := gitClient.GetRemoteHeadSHA(repoURL, branch, githubToken)
		if err != nil {
			return fmt.Errorf("failed to get remote head SHA: %w", err)
		}
		slog.Info("Remote head SHA fetched", "sha", sha)

		// Construct mock core.GitHubEvent
		event := &core.GitHubEvent{
			RepoOwner:    "", // Not directly available from CLI flags, can be derived or left empty
			RepoName:     repoURL, // Using URL as name for simplicity
			RepoFullName: repoURL, // Using URL as full name for simplicity
			RepoCloneURL: repoURL,
			HeadSHA:      sha,
			Type:         core.FullReview, // Assuming preload is always a full review
		}

		repoManager := app.RepoManager()
		ragService := app.RAGService()

		slog.Info("Syncing repository...", "repo", event.RepoName)
			_, err = repoManager.SyncRepo(ctx, event, githubToken)
			if err != nil {
				return fmt.Errorf("failed to sync repository: %w", err)
			}
			slog.Info("Repository synced successfully.")

		repoRecord, err := repoManager.GetRepoRecord(ctx, event.RepoFullName)
		if err != nil {
			return fmt.Errorf("failed to get repository record: %w", err)
		}
		if repoRecord == nil {
			return fmt.Errorf("repository record not found after sync: %s", event.RepoFullName)
		}

		// Use default RepoConfig for preload
		defaultRepoConfig := core.DefaultRepoConfig()

		slog.Info("Setting up repository context (indexing and embedding)...", "repo", event.RepoName)
		err = ragService.SetupRepoContext(ctx, defaultRepoConfig, repoRecord.QdrantCollectionName, repoRecord.ClonePath)
		if err != nil {
			return fmt.Errorf("failed to set up repository context: %w", err)
		}
		slog.Info("Repository context setup complete.")

		slog.Info("Preload complete.")
		return nil
	},
}

func init() {
	preloadCmd.Flags().StringVarP(&repoURL, "repo-url", "u", "", "Repository URL (e.g., https://github.com/owner/repo)")
	preloadCmd.Flags().StringVarP(&branch, "branch", "b", "main", "Repository branch to preload")
	preloadCmd.Flags().StringVarP(&githubToken, "github-token", "t", os.Getenv("GITHUB_TOKEN"), "GitHub Personal Access Token (or set GITHUB_TOKEN env var)")

	preloadCmd.MarkFlagRequired("repo-url")
}
