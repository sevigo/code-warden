package main

import (
	"context"
	"fmt"

	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/wire"
)

// InitializeApp initializes the application for CLI commands.
// It returns the app instance, a cleanup function, and any error encountered.
func InitializeApp(ctx context.Context, runMigrations bool) (*app.App, func(), error) {
	appInstance, cleanup, err := wire.InitializeApp(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize app: %w\n\nTip: Check that your config.yaml exists and is valid", err)
	}

	if err := appInstance.Cfg.ValidateForCLI(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	if runMigrations {
		if err := appInstance.DB.RunMigrations(); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("failed to run migrations: %w", err)
		}
	}

	return appInstance, cleanup, nil
}
