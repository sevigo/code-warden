//go:build wireinject
// +build wireinject

package wire

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/google/wire"
	"github.com/jmoiron/sqlx"
	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/db"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/jobs"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/logger"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/llms"
)

func InitializeApp(ctx context.Context) (*app.App, func(), error) {
	wire.Build(
		app.NewApp,
		server.NewServerWithStore,
		config.LoadConfig,
		db.NewDatabase,
		storage.NewStore,
		repomanager.New,
		gitutil.NewClient,
		jobs.NewDispatcher,
		jobs.NewReviewJob,
		llm.NewPromptManager,
		provideGeneratorLLM,
		provideLoggerConfig,
		provideLogWriter,
		provideDBConfig,
		provideSlogLogger,
		provideSQLXDB,
		wire.Bind(new(core.Job), new(*jobs.ReviewJob)),
	)
	return &app.App{}, nil, nil
}

func provideSQLXDB(db *db.DB) *sqlx.DB {
	return db.DB
}

func provideGeneratorLLM(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	return llm.NewGenerator(ctx, cfg.AI, logger)
}

func provideLoggerConfig(cfg *config.Config) logger.Config {
	return cfg.Logging
}

func provideDBConfig(cfg *config.Config) *config.DBConfig {
	return &cfg.Database
}

func provideLogWriter(cfg *config.Config) io.Writer {
	switch cfg.Logging.Output {
	case "stdout":
		return os.Stdout
	case "stderr":
		return os.Stderr
	case "file":
		f, err := os.OpenFile("code-warden.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			// Log to stderr since we don't have a logger yet
			fmt.Fprintf(os.Stderr, "failed to open log file: %v, falling back to stdout\n", err)
			return os.Stdout
		}
		return f
	default:
		return os.Stdout
	}
}

func provideSlogLogger(loggerConfig logger.Config, writer io.Writer) *slog.Logger {
	return logger.NewLogger(loggerConfig, writer)
}
