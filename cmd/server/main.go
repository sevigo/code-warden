package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sevigo/code-warden/internal/wire"
)

func main() {
	if err := run(); err != nil {
		fmt.Println("application failed to run", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, cleanup, err := wire.InitializeApp(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize application: %w", err)
	}
	defer cleanup()

	if err := app.Cfg.ValidateForServer(); err != nil {
		return fmt.Errorf("server configuration validation failed: %w", err)
	}

	app.Logger.Info("starting Code-Warden application")

	go func() {
		if err := app.Start(); err != nil {
			app.Logger.Error("server error", "error", err)
			cancel()
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
		app.Logger.Info("received shutdown signal")
	case <-ctx.Done():
		app.Logger.Info("context cancelled, shutting down")
	}

	if err := app.Stop(); err != nil {
		app.Logger.Error("failed to stop application", "error", err)
		return fmt.Errorf("failed to stop application: %w", err)
	}
	return nil
}
