// Package app initializes and orchestrates the main components of the Code Warden application.
// It wires together the configuration, server, and other services.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/parsers"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/db"
	"github.com/sevigo/code-warden/internal/jobs"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
)

// App holds the main application components.
type App struct {
	ctx        context.Context
	cfg        *config.Config
	server     *server.Server
	logger     *slog.Logger
	dispatcher core.JobDispatcher
	dbConn     *db.DB
	repoManager repomanager.RepoManager
	ragService llm.RAGService
}

// newOllamaHTTPClient creates an HTTP client with longer timeouts for Ollama requests.
// Ollama can take a while to process requests, so we need more generous timeouts.
func newOllamaHTTPClient() *http.Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxConnsPerHost:     10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   false,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   5 * time.Minute,
	}
}

// NewApp sets up the application with all its dependencies.
func NewApp(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*App, func(), error) {
	logger.Info("initializing Code Warden application",
		"llm_provider", cfg.LLMProvider,
		"generator_model", cfg.GeneratorModelName,
		"embedder_model", cfg.EmbedderModelName,
		"max_workers", cfg.MaxWorkers,
		"repo_path", cfg.RepoPath,
	)

	dbConn, dbCleanup, err := initDatabase(cfg.Database)
	if err != nil {
		return nil, nil, err
	}

	store := storage.NewStore(dbConn.DB)

	repoManager := repomanager.New(cfg, store, logger)

	generatorLLM, err := createGeneratorLLM(ctx, cfg, logger)
	if err != nil {
		dbCleanup()
		return nil, nil, err
	}
	embedder, err := createEmbedder(cfg, logger)
	if err != nil {
		dbCleanup()
		return nil, nil, err
	}
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
	vectorStore := storage.NewQdrantVectorStore(cfg.QdrantHost, embedder, logger)
	ragService := llm.NewRAGService(cfg, promptMgr, vectorStore, generatorLLM, parserRegistry, logger)

	reviewJob := jobs.NewReviewJob(cfg, ragService, store, repoManager, logger)

	// TODO(follow-up): Initialize and start the repository cleanup service (janitor).
	// This service will periodically scan for and delete old/unused repositories
	// and their associated Qdrant collections to manage long-term resource usage.
	// The implementation plan is documented in `TODO.md`.

	dispatcher := jobs.NewDispatcher(ctx, reviewJob, cfg.MaxWorkers, logger)
	httpServer := server.NewServer(ctx, cfg, dispatcher, logger)

	logger.Info("Code Warden application initialized successfully")
	return &App{
			ctx:        ctx,
			cfg:        cfg,
			server:     httpServer,
			logger:     logger,
			dispatcher: dispatcher,
			dbConn:     dbConn,
			repoManager: repoManager,
			ragService: ragService,
		}, func() {
			dbCleanup()
		}, nil
}

// RepoManager returns the repository manager instance.
func (a *App) RepoManager() repomanager.RepoManager {
	return a.repoManager
}

// RAGService returns the RAG service instance.
func (a *App) RAGService() llm.RAGService {
	return a.ragService
}

func createGeneratorLLM(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	logger.Info("connecting to generator LLM", "model", cfg.GeneratorModelName)
	llm, err := createLLM(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to connect to generator LLM", "error", err)
		return nil, fmt.Errorf("failed to create generator LLM: %w", err)
	}
	return llm, nil
}

func createEmbedder(cfg *config.Config, logger *slog.Logger) (embeddings.Embedder, error) {
	logger.Info("connecting to embedder LLM", "model", cfg.EmbedderModelName, "host", cfg.OllamaHost)
	embedderLLM, err := ollama.New(
		ollama.WithServerURL(cfg.OllamaHost),
		ollama.WithModel(cfg.EmbedderModelName),
		ollama.WithHTTPClient(newOllamaHTTPClient()),
		ollama.WithLogger(logger),
	)
	if err != nil {
		logger.Error("failed to connect to embedder LLM", "error", err)
		return nil, fmt.Errorf("failed to create embedder LLM: %w", err)
	}

	embedder, err := embeddings.NewEmbedder(embedderLLM)
	if err != nil {
		logger.Error("failed to create embedder service", "error", err)
		return nil, fmt.Errorf("failed to create embedder: %w", err)
	}
	return embedder, nil
}

// Start runs the HTTP server.
func (a *App) Start() error {
	a.logger.Info("starting Code Warden",
		"server_port", a.cfg.ServerPort,
		"max_workers", a.cfg.MaxWorkers)

	err := a.server.Start()
	if err != nil {
		a.logger.Error("failed to start HTTP server", "error", err)
		return err
	}

	return nil
}

// Stop shuts down the application cleanly.
func (a *App) Stop() error {
	var shutdownErr error
	a.logger.Info("shutting down Code Warden services")

	// Stop the HTTP server first to prevent new incoming requests.
	serverErr := a.server.Stop()
	if serverErr != nil {
		a.logger.Error("error during HTTP server shutdown", "error", serverErr)
		shutdownErr = errors.Join(shutdownErr, serverErr)
	}

	// Stop the job dispatcher, allowing in-flight jobs to finish.
	a.dispatcher.Stop()

	a.logger.Info("closing database connection")
	dbCloseErr := a.dbConn.Close()
	if dbCloseErr != nil {
		a.logger.Error("error closing database", "error", dbCloseErr)
		shutdownErr = errors.Join(shutdownErr, dbCloseErr)
	}

	if shutdownErr != nil {
		a.logger.Error("Code Warden stopped with errors", "error", shutdownErr)
	} else {
		a.logger.Info("Code Warden stopped successfully")
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

// createLLM creates the appropriate LLM client based on the configured provider.
func createLLM(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	switch cfg.LLMProvider {
	case "gemini":
		logger.Info("Using Gemini LLM provider", "model", cfg.GeneratorModelName)
		if cfg.GeminiAPIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is not set in environment for gemini provider")
		}
		return gemini.New(ctx,
			gemini.WithModel(cfg.GeneratorModelName),
			gemini.WithAPIKey(cfg.GeminiAPIKey),
		)

	case "ollama":
		logger.Info("Using Ollama LLM provider", "model", cfg.GeneratorModelName)
		return ollama.New(
			ollama.WithHTTPClient(newOllamaHTTPClient()),
			ollama.WithModel(cfg.GeneratorModelName),
			ollama.WithLogger(logger),
		)

	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.LLMProvider)
	}
}
