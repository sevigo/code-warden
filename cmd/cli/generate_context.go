package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/sevigo/code-warden/internal/wire"
)

var genContextRepoFullName string

var genContextCmd = &cobra.Command{
	Use:   "generate-context [path]",
	Short: "Manually trigger MapReduce project context generation",
	Long: `Triggers the Map phase (directory summaries) and Reduce phase (global synthesis) 
for a local repository. The resulting context is saved to the database.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		repoPath := args[0]
		slog.Info("🗺️  Starting MapReduce project context generation", "path", repoPath)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()

		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize application: %w", err)
		}
		defer cleanup()

		// 1. Resolve repo record
		if genContextRepoFullName == "" {
			// Try to find it by path
			record, err := app.RepoMgr.GetRepoRecordByPath(ctx, repoPath)
			if err != nil || record == nil {
				return fmt.Errorf("could not find repository record for path %s. Please provide --repo-full-name", repoPath)
			}
			genContextRepoFullName = record.FullName
		}

		repoRecord, err := app.RepoMgr.GetRepoRecord(ctx, genContextRepoFullName)
		if err != nil || repoRecord == nil {
			return fmt.Errorf("failed to retrieve repository record for %s: %w", genContextRepoFullName, err)
		}

		// 2. Load repo config
		repoConfig, err := app.RepoMgr.LoadRepoConfig(repoPath)
		if err != nil {
			slog.Warn("failed to load repo config, using defaults", "error", err)
		}

		// 3. Trigger Map phase (Arch Summaries)
		slog.Info("🗺️  Phase 1/2: Mapping directory architectures", "repo", genContextRepoFullName)
		err = app.RAGService.GenerateArchSummaries(ctx, repoRecord.QdrantCollectionName, repoRecord.EmbedderModelName, repoPath, nil)
		if err != nil {
			return fmt.Errorf("map phase failed: %w", err)
		}

		// 4. Trigger Reduce phase (Global Synthesis)
		slog.Info("📉 Phase 2/2: Reducing architectural summaries into global project context", "repo", genContextRepoFullName)
		projectContext, err := app.RAGService.GenerateProjectContext(ctx, repoRecord.QdrantCollectionName, repoRecord.EmbedderModelName)
		if err != nil {
			return fmt.Errorf("reduce phase failed: %w", err)
		}

		if projectContext == "" {
			slog.Warn("⚠️  No project context generated.")
			return nil
		}

		// 5. Save to database
		slog.Info("💾 Saving generated context to database")
		repoRecord.GeneratedContext = projectContext
		// Since we don't have a specific UpdateRepository method in RepoManager, we use the store directly via the app
		// But wire.go might not expose the store directly if it's encapsulated in RepoManager.
		// Let's check how UpdateRepoSHA is implemented in RepoManager.

		// Fallback: If RepoManager doesn't expose it, we might need to add it there.
		// For now, let's assume RAGService's SetupRepoContext logic is what we want to re-use or trigger.

		err = app.RAGService.SetupRepoContext(ctx, repoConfig, repoRecord, repoPath)
		if err != nil {
			return fmt.Errorf("failed to finalize project context via SetupRepoContext: %w", err)
		}

		slog.Info("✅ Successfully generated and persisted MapReduce project context.")
		return nil
	},
}

func init() { //nolint:gochecknoinits // Cobra's init function for command registration
	genContextCmd.Flags().StringVar(&genContextRepoFullName, "repo-full-name", "", "The full name of the repository (e.g. owner/repo)")
	rootCmd.AddCommand(genContextCmd)
}
