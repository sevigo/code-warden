package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/sevigo/code-warden/internal/wire"
)

func main() {
	if err := run(); err != nil {
		slog.Error("cli failed to run", "error", err)
		os.Exit(1)
	}
}

func run() error {
	_, cleanup, err := wire.InitializeApp(context.Background())
	if err != nil {
		return fmt.Errorf("failed to initialize app: %w", err)
	}
	defer cleanup()

	return nil
}