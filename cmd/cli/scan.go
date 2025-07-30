package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/code-warden/internal/wire"
	"github.com/spf13/cobra"
)

var scanCmd = &cobra.Command{
	Use:   "scan [path]",
	Short: "Scan a local git repository.",
	Long:  `Scans a local git repository at the given path.`,
	Args:  cobra.ExactArgs(1),
	Run: func(_ *cobra.Command, args []string) {
		repoPath := args[0]
		fmt.Printf("Scanning local repository at: %s\n", repoPath)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			slog.Error("failed to initialize application", "error", err)
			return
		}
		defer cleanup()

		if err := app.RepoMgr.ScanLocalRepo(ctx, repoPath); err != nil {
			slog.Error("failed to scan local repository", "error", err)
			return
		}
		slog.Info("Successfully scanned local repository")
	},
}
