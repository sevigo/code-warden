package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/wire"
)

var (
	repoFullName string
	forceScan    bool
)

// Custom error types for better error handling
var (
	ErrConfigNotFound = errors.New("config file not found")
	ErrConfigParsing  = errors.New("config parsing failed")
)

var scanCmd = &cobra.Command{
	Use:   "scan [path]",
	Short: "Scan a local git repository.",
	Long:  `Scans a local git repository at the given path, updating the vector store with any changes.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		repoPath := args[0]
		slog.Info("Scanning local repository", "path", repoPath, "force", forceScan)

		// Use a context with a timeout for robustness in a long-running CLI command.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize application: %w", err)
		}
		defer cleanup()

		// 1. Scan the local repo to get the list of changed files.
		// The `forceScan` flag will ensure this result comes back with IsInitialClone=true
		// if a full re-index is needed.
		updateResult, err := app.RepoMgr.ScanLocalRepo(ctx, repoPath, repoFullName, forceScan)
		if err != nil {
			return fmt.Errorf("failed to scan local repository: %w", err)
		}
		slog.Info("Local repository scan complete", "repo", updateResult.RepoFullName, "head_sha", updateResult.HeadSHA)

		// 2. Load the repository's .code-warden.yml configuration
		repoConfig, err := loadRepoConfig(updateResult.RepoPath)
		if err != nil {
			if errors.Is(err, ErrConfigNotFound) {
				slog.Info("no .code-warden.yml found, using defaults", "repo", updateResult.RepoFullName)
			} else {
				slog.Warn("failed to parse .code-warden.yml, using defaults", "error", err, "repo", updateResult.RepoFullName)
			}
			repoConfig = core.DefaultRepoConfig()
		}

		// 3. Get repository record to find the collection name
		repoRecord, err := app.RepoMgr.GetRepoRecord(ctx, updateResult.RepoFullName)
		if err != nil {
			return fmt.Errorf("failed to retrieve repository record: %w", err)
		}
		if repoRecord == nil {
			return fmt.Errorf("repository record is unexpectedly nil for %s", updateResult.RepoFullName)
		}
		collectionName := repoRecord.QdrantCollectionName

		// 4. Update the vector store with the changes
		slog.Info("Updating vector store", "collection", collectionName, "is_full_scan", updateResult.IsInitialClone)
		switch {
		case updateResult.IsInitialClone:
			slog.Info("Performing initial full indexing")
			err = app.RAGService.SetupRepoContext(ctx, repoConfig, collectionName, updateResult.RepoPath)
		case len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0:
			slog.Info("Performing incremental indexing",
				"add_or_update", len(updateResult.FilesToAddOrUpdate),
				"delete", len(updateResult.FilesToDelete),
			)
			err = app.RAGService.UpdateRepoContext(ctx, repoConfig, collectionName, updateResult.RepoPath, updateResult.FilesToAddOrUpdate, updateResult.FilesToDelete)
		default:
			slog.Info("No file changes detected, skipping vector store update.")
		}
		if err != nil {
			return fmt.Errorf("failed to update vector store: %w", err)
		}

		// 5. Update the last indexed SHA in the database to the new HEAD SHA.
		// This is the *only* place this should happen to ensure data consistency.
		slog.Info("Updating last indexed SHA in database", "sha", updateResult.HeadSHA)
		if err := app.RepoMgr.UpdateRepoSHA(ctx, updateResult.RepoFullName, updateResult.HeadSHA); err != nil {
			return fmt.Errorf("CRITICAL: vector store updated but failed to update SHA in database: %w", err)
		}

		slog.Info("✅ Successfully scanned local repository and updated RAG system.")
		return nil
	},
}

func init() { //nolint:gochecknoinits // Cobra's init function for command registration
	scanCmd.Flags().StringVar(&repoFullName, "repo-full-name", "", "The full name of the repository (e.g. owner/repo)")
	scanCmd.Flags().BoolVar(&forceScan, "force", false, "Force a full re-scan and re-indexing of the repository, ignoring the last indexed state.")
	rootCmd.AddCommand(scanCmd)
}

func loadRepoConfig(repoPath string) (*core.RepoConfig, error) {
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

	slog.Info(".code-warden.yml loaded successfully", "repo_path", repoPath)
	return config, nil
}
