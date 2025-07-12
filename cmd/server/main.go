package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/config"
)

func main() {
	if err := run(); err != nil {
		slog.Error("application failed to run", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	slog.Info("starting Code-Warden application")

	application, err := app.NewApp(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize application: %w", err)
	}

	go func() {
		if err := application.Start(); err != nil {
			slog.Error("server error", "error", err)
			cancel()
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
		slog.Info("received shutdown signal")
	case <-ctx.Done():
		slog.Info("context cancelled, shutting down")
	}

	if err := application.Stop(); err != nil {
		slog.Error("failed to stop application", "error", err)
		return fmt.Errorf("failed to stop application: %w", err)
	}
	return nil
}
