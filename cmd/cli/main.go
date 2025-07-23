package main

import (
	"log/slog"
	"os"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		slog.Error("cli failed to run", "error", err)
		os.Exit(1)
	}
}
