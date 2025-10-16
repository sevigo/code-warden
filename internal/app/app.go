package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/vectorstores/qdrant"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/db"

	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/jobs"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
)

const (
	llmProviderGemini = "gemini"
)

// App holds the main application components.
type App struct {
	Store      storage.Store
	RepoMgr    repomanager.RepoManager
	RAGService llm.RAGService
	GitClient  *gitutil.Client
	Cfg        *config.Config
	Logger     *slog.Logger
	server     *server.Server
	dispatcher core.JobDispatcher
}

// NewApp sets up the application with all its dependencies.
func NewApp(
	ctx context.Context,
	cfg *config.Config,
	generatorLLM llms.Model,
	embedder embeddings.Embedder,
	logger *slog.Logger,
) (*App, func(), error) {
	logger.Info("initializing Code Warden application",
		"llm_provider", cfg.LLMProvider,
		"embedder_provider", cfg.EmbedderProvider,
		"generator_model", cfg.GeneratorModelName,
		"embedder_model", cfg.EmbedderModelName,
		"max_workers", cfg.MaxWorkers,
		"repo_path", cfg.RepoPath,
		"llm_provider", cfg.LLMProvider,
		"embedder_provider", cfg.EmbedderProvider,
	)

	dbConn, dbCleanup, err := initDatabase(cfg.Database)
	if err != nil {
		return nil, nil, err
	}

	store := storage.NewStore(dbConn.DB)
	gitClient := gitutil.NewClient(logger.With("component", "gitutil"))

	parserRegistry, err := parsers.RegisterLanguagePlugins(logger)
	if err != nil {
		dbCleanup()
		return nil, nil, fmt.Errorf("failed to register language parsers: %w", err)
	}
	promptMgr, err := llm.NewPromptManager()
	if err != nil {
		dbCleanup()
		return nil, nil, fmt.Errorf("failed to initialize prompt manager: %w", err)
	}

	// Select the batch config based on the embedder provider to handle rate limits.
	var batchConfig *qdrant.BatchConfig
	// External APIs (Gemini, FastAPI) use a more conservative batching strategy.
	if cfg.EmbedderProvider == llmProviderGemini {
		logger.Info("using conservative batch config for external provider", "provider", cfg.EmbedderProvider)
		// Slower, sequential processing to respect potential API rate limits.
		batchConfig = &qdrant.BatchConfig{
			BatchSize:               256,
			MaxConcurrency:          4,
			EmbeddingBatchSize:      90,
			EmbeddingMaxConcurrency: 1,
			RetryAttempts:           qdrant.DefaultRetryAttempts,
			RetryDelay:              qdrant.DefaultRetryDelay,
			RetryJitter:             qdrant.DefaultRetryJitter,
			MaxRetryDelay:           qdrant.DefaultMaxRetryDelay,
		}
	} else {
		logger.Info("using aggressive batch config for local provider", "provider", cfg.EmbedderProvider)
		batchConfig = &qdrant.BatchConfig{
			BatchSize:               512,
			MaxConcurrency:          8,
			EmbeddingBatchSize:      64,
			EmbeddingMaxConcurrency: 8,
			RetryAttempts:           2,
			RetryDelay:              1 * time.Second,
		}
	}

	vectorStore := storage.NewQdrantVectorStore(
		cfg,
		logger,
		storage.WithBatchConfig(batchConfig),
		storage.WithInitialEmbedder(cfg.EmbedderModelName, embedder),
	)
	repoManager := repomanager.New(cfg, store, vectorStore, gitClient, logger.With("component", "repomanager"))

	ragService := llm.NewRAGService(cfg, promptMgr, vectorStore, store, generatorLLM, parserRegistry, logger)
	reviewJob := jobs.NewReviewJob(cfg, ragService, store, repoManager, logger)

	dispatcher := jobs.NewDispatcher(ctx, reviewJob, cfg.MaxWorkers, logger)
	httpServer := server.NewServer(ctx, cfg, dispatcher, logger)

	logger.Info("Code Warden application initialized successfully")
	return &App{
			Store:      store,
			RepoMgr:    repoManager,
			RAGService: ragService,
			GitClient:  gitClient,
			Logger:     logger,
			server:     httpServer,
			dispatcher: dispatcher,
			Cfg:        cfg,
		}, func() {
			dbCleanup()
		}, nil
}

// Start runs the HTTP server.
func (a *App) Start() error {
	a.Logger.Info("starting Code Warden",
		"server_port", a.Cfg.ServerPort,
		"max_workers", a.Cfg.MaxWorkers)

	err := a.server.Start()
	if err != nil {
		a.Logger.Error("failed to start HTTP server", "error", err)
		return err
	}

	return nil
}

// Stop shuts down the application cleanly.
func (a *App) Stop() error {
	var shutdownErr error
	a.Logger.Info("shutting down Code Warden services")

	// Stop the job dispatcher, allowing in-flight jobs to finish.
	a.dispatcher.Stop()

	// Stop the HTTP server to prevent new incoming requests.
	if a.server != nil {
		serverErr := a.server.Stop()
		if serverErr != nil {
			a.Logger.Error("error during HTTP server shutdown", "error", serverErr)
			shutdownErr = errors.Join(shutdownErr, serverErr)
		}
	}

	if shutdownErr != nil {
		a.Logger.Error("Code Warden stopped with errors", "error", shutdownErr)
	} else {
		a.Logger.Info("Code Warden stopped successfully")
	}
	return shutdownErr
}

// initDatabase connects to the DB and runs migrations
func initDatabase(cfg *config.DBConfig) (*db.DB, func(), error) {
	dbConn, cleanup, err := db.NewDatabase(cfg)
	if err != nil {
		return nil, func() {}, fmt.Errorf("failed to connect to database: %w", err)
	}
	if err := dbConn.RunMigrations(); err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("failed to run database migrations: %w", err)
	}
	return dbConn, cleanup, nil
}
