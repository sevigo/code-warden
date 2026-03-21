package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/sevigo/code-warden/internal/prescan"
)

var (
	prescanForce               bool
	prescanVerbose             bool
	prescanGenerateContextOnly bool
)

var prescanCmd = &cobra.Command{
	Use:   "prescan [path_or_url]",
	Short: "Scan a repository (local or remote) with resume capability.",
	Long:  `Scans a repository. If a URL is provided, checks out to managed storage. Supports auto-resume.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		input := args[0]
		slog.Info("Initiating pre-scan", "input", input, "force", prescanForce)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour) // Long timeout for large repos
		defer cancel()

		app, cleanup, err := InitializeApp(ctx, false)
		if err != nil {
			return err
		}
		defer cleanup()

		// Initialize Prescan Components
		// We could wire this in wire.go, but for now construct manually using app dependencies
		prescanMgr := prescan.NewManager(app.Cfg, app.Store, app.GitClient, slog.Default())
		scanner := prescan.NewScanner(prescanMgr, app.RAGService)

		if err := scanner.Scan(ctx, input, prescanForce, prescanVerbose, prescanGenerateContextOnly); err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}

		slog.Info("✅ Pre-scan completed successfully.")
		return nil
	},
}

func init() { //nolint:gochecknoinits // Cobra's init function for command registration
	prescanCmd.Flags().BoolVar(&prescanForce, "force", false, "Force restart of scan, ignoring previous state.")
	prescanCmd.Flags().BoolVarP(&prescanVerbose, "verbose", "v", false, "Show detailed progress for each file.")
	prescanCmd.Flags().BoolVar(&prescanGenerateContextOnly, "generate-context-only", false, "Only run the Project Context generation step (requires a previously indexed repo).")
	rootCmd.AddCommand(prescanCmd)
}
