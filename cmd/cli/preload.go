package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/wire"
)

var (
	repoURL string
	branch  string
)

var preloadCmd = &cobra.Command{
	Use:   "preload",
	Short: "Preloads and indexes a repository into Code-Warden",
	Long: `This command clones a repository, creates the necessary database entries,
and performs an initial scan to populate the vector store. This is useful for
avoiding cold-start delays on large repositories.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()

		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize app services: %w", err)
		}
		defer cleanup()

		token := viper.GetString("GITHUB_TOKEN")
		if token == "" {
			return fmt.Errorf("a GitHub token is required; please provide it via the --github-token flag or the GITHUB_TOKEN environment variable")
		}

		fmt.Printf("Preloading repository %s/%s\n", repoURL, branch)

		// Get remote head SHA
		fmt.Printf("Fetching remote SHA for branch %q\n", branch)
		headSHA, err := app.GitClient.GetRemoteHeadSHA(repoURL, branch, token)
		if err != nil {
			return fmt.Errorf("failed to get remote head SHA: %w", err)
		}
		fmt.Printf("Successfully fetched remote SHA %q\n", headSHA)

		// Construct mock GitHubEvent
		repoFullName, err := parseRepoFullName(repoURL)
		if err != nil {
			return err
		}
		mockEvent := &core.GitHubEvent{
			RepoCloneURL: repoURL,
			RepoFullName: repoFullName,
			HeadSHA:      headSHA,
		}

		// Call RepoManager to sync (clone) the repo
		fmt.Println("Cloning repository and creating database record...")
		updateResult, err := app.RepoMgr.SyncRepo(ctx, mockEvent, token)
		if err != nil {
			return fmt.Errorf("failed to sync repository: %w", err)
		}

		// Call RAGService to perform initial indexing
		repoRecord, err := app.RepoMgr.GetRepoRecord(ctx, repoFullName)
		if err != nil {
			return fmt.Errorf("failed to retrieve repository record after sync: %w", err)
		}

		fmt.Printf("Performing initial indexing and embedding... collection: %s\n", repoRecord.QdrantCollectionName)
		err = app.RAGService.SetupRepoContext(ctx, core.DefaultRepoConfig(), repoRecord.QdrantCollectionName, updateResult.RepoPath)
		if err != nil {
			return fmt.Errorf("failed to setup repository context: %w", err)
		}

		fmt.Printf("\n✅ Successfully preloaded repository '%s'.\n", repoFullName)
		fmt.Printf("   Qdrant Collection: %s\n", repoRecord.QdrantCollectionName)

		return nil
	},
}

func init() { //nolint:gochecknoinits // Cobra's init function for command registration
	preloadCmd.Flags().StringVarP(&repoURL, "repo-url", "u", "", "URL of the repository to preload")
	preloadCmd.Flags().StringVarP(&branch, "branch", "b", "main", "Branch to preload")
	if err := preloadCmd.MarkFlagRequired("repo-url"); err != nil {
		slog.Error("Error marking flag as required", "error", err)
		os.Exit(1)
	}
	rootCmd.AddCommand(preloadCmd)
}

func parseRepoFullName(repoURL string) (string, error) {
	parsedURL, err := url.Parse(repoURL)
	if err != nil {
		return "", fmt.Errorf("invalid repository URL: %w", err)
	}

	path := strings.TrimSuffix(parsedURL.Path, ".git")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 {
		return fmt.Sprintf("%s/%s", parts[0], parts[1]), nil
	}
	return "", fmt.Errorf("cannot parse owner/repo from URL: %s", repoURL)
}
