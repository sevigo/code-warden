package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/wire"
)

var (
	updateRepoFullName string
	updateForce        bool
)

// Custom error types for better error handling
var (
	ErrUpdateConfigNotFound = errors.New("config file not found")
	ErrUpdateConfigParsing  = errors.New("config parsing failed")
)

var updateCmd = &cobra.Command{
	Use:   "update [path]",
	Short: "Incrementally update the vector store for a local repository",
	Long: `Updates the vector store for a local git repository at the given path.
This command uses Git diffs to identify files that have changed since the last 
successful update, performing efficient incremental indexing. 

If the repository has never been indexed, it will perform an initial full scan.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		repoPath := args[0]
		slog.Info("Updating local repository", "path", repoPath, "force", updateForce)

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize application: %w", err)
		}
		defer cleanup()

		updateResult, err := app.RepoMgr.ScanLocalRepo(ctx, repoPath, updateRepoFullName, updateForce)
		if err != nil {
			return fmt.Errorf("failed to scan local repository for update: %w", err)
		}
		slog.Info("Local repository update analysis complete", "repo", updateResult.RepoFullName, "head_sha", updateResult.HeadSHA)

		repoConfig, err := config.LoadRepoConfig(updateResult.RepoPath)
		if err != nil {
			if errors.Is(err, config.ErrConfigNotFound) {
				slog.Info("no .code-warden.yml found, using defaults", "repo", updateResult.RepoFullName)
			} else {
				slog.Warn("failed to parse .code-warden.yml, using defaults", "error", err, "repo", updateResult.RepoFullName)
			}
			repoConfig = core.DefaultRepoConfig()
		}

		repoRecord, err := app.RepoMgr.GetRepoRecord(ctx, updateResult.RepoFullName)
		if err != nil {
			return fmt.Errorf("failed to retrieve repository record: %w", err)
		}
		if repoRecord == nil {
			return fmt.Errorf("repository record is unexpectedly nil for %s", updateResult.RepoFullName)
		}
		collectionName := repoRecord.QdrantCollectionName

		// Update the vector store with the changes
		slog.Info("Updating vector store", "collection", collectionName, "is_full_scan", updateResult.IsInitialClone)

		switch {
		case updateResult.IsInitialClone:
			slog.Info("Performing initial full indexing")
			err = app.RAGService.SetupRepoContext(
				ctx,
				repoConfig,
				repoRecord,
				updateResult.RepoPath,
				nil,
			)

		case len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0:
			slog.Info("Performing incremental indexing",
				"add_or_update", len(updateResult.FilesToAddOrUpdate),
				"delete", len(updateResult.FilesToDelete),
			)
			err = app.RAGService.UpdateRepoContext(
				ctx,
				repoConfig,
				repoRecord,
				updateResult.RepoPath,
				updateResult.FilesToAddOrUpdate,
				updateResult.FilesToDelete,
			)

		default:
			slog.Info("No file changes detected, skipping vector store update.")
		}
		if err != nil {
			return fmt.Errorf("failed to update vector store: %w", err)
		}

		// Update the last indexed SHA in the database to the new HEAD SHA.
		slog.Info("Updating last indexed SHA in database", "sha", updateResult.HeadSHA)
		if err := app.RepoMgr.UpdateRepoSHA(ctx, updateResult.RepoFullName, updateResult.HeadSHA); err != nil {
			return fmt.Errorf("CRITICAL: vector store updated but failed to update SHA in database: %w", err)
		}

		slog.Info("✅ Successfully updated local repository in RAG system.")
		return nil
	},
}

func init() { //nolint:gochecknoinits // Cobra's init function for command registration
	updateCmd.Flags().StringVar(&updateRepoFullName, "repo-full-name", "", "The full name of the repository (e.g. owner/repo)")
	updateCmd.Flags().BoolVar(&updateForce, "force", false, "Force a full re-scan and re-indexing of the repository, ignoring the last indexed state.")
	rootCmd.AddCommand(updateCmd)
}
