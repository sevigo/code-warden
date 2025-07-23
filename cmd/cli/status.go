package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/sevigo/code-warden/internal/wire"
	"github.com/spf13/cobra"
)

var outputJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Shows the status of all repositories managed by Code-Warden",
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx := context.Background()

		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize app services: %w", err)
		}
		defer cleanup()

		repos, err := app.Store.GetAllRepositories(ctx)
		if err != nil {
			return fmt.Errorf("failed to retrieve repositories: %w", err)
		}

		if outputJSON {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(repos)
		}

		if len(repos) == 0 {
			slog.Info("No repositories are currently managed by Code-Warden.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "REPOSITORY\tLAST INDEXED SHA\tQDRANT COLLECTION\tLAST UPDATED")
		for _, repo := range repos {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				repo.FullName,
				repo.LastIndexedSHA[:7], // Short SHA
				repo.QdrantCollectionName,
				repo.UpdatedAt.Format(time.RFC822),
			)
		}
		return w.Flush()
	},
}

func init() { //nolint:gochecknoinits // Cobra's init function for command registration
	statusCmd.Flags().BoolVar(&outputJSON, "json", false, "Output status as JSON")
	rootCmd.AddCommand(statusCmd)
}
