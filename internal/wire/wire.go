//go:build wireinject
// +build wireinject

package wire

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/wire"
	"github.com/jmoiron/sqlx"
	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/db"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/globalmcp"
	"github.com/sevigo/code-warden/internal/jobs"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/logger"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/llms/openai"
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
		provideGlobalMCPServer,
		provideWorkspaceRegistry,

		wire.Bind(new(core.Job), new(*jobs.ReviewJob)),
		wire.Bind(new(core.SessionCanceller), new(*jobs.ReviewJob)),
	)
	return &app.App{}, nil, nil
}

func parseHeaderTimeout(s string, logger *slog.Logger) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		logger.Warn("invalid http_response_header_timeout, using default 180s", "error", err)
		return 180 * time.Second
	}
	return d
}

func parseRequestTimeout(s string, logger *slog.Logger) time.Duration {
	if s == "" {
		return 0 // No timeout
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		logger.Warn("invalid http_request_timeout, using no timeout", "error", err)
		return 0
	}
	return d
}

func provideSQLXDB(db *db.DB) *sqlx.DB {
	return db.DB
}

func provideGeneratorLLM(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	switch cfg.AI.LLMProvider {
	case "gemini":
		if cfg.AI.GeminiAPIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is not set")
		}
		return gemini.New(ctx, gemini.WithModel(cfg.AI.GeneratorModel), gemini.WithAPIKey(cfg.AI.GeminiAPIKey))
	case "openai":
		if cfg.AI.OpenAIAPIKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is not set")
		}
		baseURL := cfg.AI.OpenAIBaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		modelName := cfg.AI.GeneratorModel
		if cfg.AI.OpenAIModel != "" {
			modelName = cfg.AI.OpenAIModel
		}
		return openai.New(
			openai.WithAPIKey(cfg.AI.OpenAIAPIKey),
			openai.WithBaseURL(baseURL),
			openai.WithModel(modelName),
			openai.WithLogger(logger),
		)
	case "ollama":
		headerTimeout := parseHeaderTimeout(cfg.AI.HTTPResponseHeaderTimeout, logger)
		requestTimeout := parseRequestTimeout(cfg.AI.HTTPRequestTimeout, logger)

		logger.Info("configuring Ollama for generator",
			"response_header_timeout", headerTimeout,
			"request_timeout", requestTimeout,
			"model", cfg.AI.GeneratorModel,
		)

		opts := llm.BuildOllamaOptions(llm.OllamaClientConfig{
			ServerURL:          cfg.AI.OllamaHost,
			APIKey:             cfg.AI.OllamaAPIKey,
			Model:              cfg.AI.GeneratorModel,
			HTTPHeaderTimeout:  headerTimeout,
			HTTPRequestTimeout: requestTimeout,
			ModelKeepAlive:     cfg.AI.ModelKeepAlive,
			EnableThinking:     cfg.AI.EnableThinking,
			ThinkingEffort:     cfg.AI.ThinkingEffort,
			Logger:             logger,
		})
		return ollama.New(opts...)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.AI.LLMProvider)
	}
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

func provideGlobalMCPServer(ctx context.Context, cfg *config.Config, logger *slog.Logger, registry *globalmcp.WorkspaceRegistry, store storage.Store) (*globalmcp.Server, error) {
	if cfg.Agent.DefaultWorkspace == "" {
		logger.Info("No default workspace configured, using proxy-only MCP server")
		return globalmcp.NewServer(cfg, logger, registry), nil
	}

	logger.Info("Default workspace configured, initializing standalone MCP server",
		"workspace", cfg.Agent.DefaultWorkspace,
		"repo", cfg.Agent.DefaultWorkspaceRepo)

	repo, err := getOrCreateDefaultRepo(ctx, store, cfg.Agent.DefaultWorkspaceRepo, cfg.Agent.DefaultWorkspace, logger)
	if err != nil {
		logger.Error("Failed to setup default workspace", "error", err)
		return nil, fmt.Errorf("failed to setup default workspace: %w", err)
	}

	standaloneCfg := &globalmcp.StandaloneConfig{
		Store:      store,
		Repo:       repo,
		RepoConfig: core.DefaultRepoConfig(),
	}

	return globalmcp.NewStandaloneServer(cfg, logger, registry, standaloneCfg), nil
}

func getOrCreateDefaultRepo(ctx context.Context, store storage.Store, repoFullName, repoPath string, logger *slog.Logger) (*storage.Repository, error) {
	repo, err := store.GetRepositoryByFullName(ctx, repoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing repository: %w", err)
	}

	if repo != nil {
		logger.Info("Found existing repository record for default workspace", "repo", repoFullName)
		return repo, nil
	}

	logger.Info("Creating new repository record for default workspace", "repo", repoFullName)

	repo = &storage.Repository{
		FullName:  repoFullName,
		ClonePath: repoPath,
	}

	if err := store.CreateRepository(ctx, repo); err != nil {
		return nil, fmt.Errorf("failed to create repository record: %w", err)
	}

	return repo, nil
}

func provideWorkspaceRegistry(logger *slog.Logger) *globalmcp.WorkspaceRegistry {
	return globalmcp.NewWorkspaceRegistry(logger)
}
