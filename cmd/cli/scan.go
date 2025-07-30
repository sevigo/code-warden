package main

import (
	"context"
	"log/slog"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/wire"
	"github.com/spf13/cobra"
)

var repoFullName string

var scanCmd = &cobra.Command{
	Use:   "scan [path]",
	Short: "Scan a local git repository.",
	Long:  `Scans a local git repository at the given path.`,
	Args:  cobra.ExactArgs(1),
	Run: func(_ *cobra.Command, args []string) {
		repoPath := args[0]
		slog.Info("Scanning local repository", "path", repoPath)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			slog.Error("failed to initialize application", "error", err)
			return
		}
		defer cleanup()

		updateResult, err := app.RepoMgr.ScanLocalRepo(ctx, repoPath, repoFullName)
		if err != nil {
			slog.Error("failed to scan local repository", "error", err)
			return
		}

		// Dispatch a review job to process the files.
		job := &core.GitHubEvent{
			RepoFullName: updateResult.RepoFullName,
			RepoCloneURL: updateResult.RepoPath, // For local scans, RepoCloneURL is the local path
			HeadSHA:      updateResult.HeadSHA,
			IsLocalScan:  true,
			// Other fields can be left at their default values or populated if needed for local scans
		}
		if err := app.Dispatcher.Dispatch(ctx, job); err != nil {
			slog.Error("failed to dispatch review job", "error", err)
			return
		}

		slog.Info("Successfully scanned local repository and dispatched review job")
	},
}
