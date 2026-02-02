// Code generated manually. DO NOT EDIT.

//go:generate go run -mod=mod github.com/google/wire/cmd/wire
//go:build !wireinject
// +build !wireinject

package wire

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/db"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/jobs"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/logger"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/vectorstores/qdrant"
)

// InitializeApp creates and wires all application dependencies.
func InitializeApp(ctx context.Context) (*app.App, func(), error) {
	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Setup logger
	loggerConfig := cfg.Logging
	var logWriter io.Writer
	switch cfg.Logging.Output {
	case "stderr":
		logWriter = os.Stderr
	case "file":
		f, _ := os.OpenFile("code-warden.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		logWriter = f
	default:
		logWriter = os.Stdout
	}
	slogLogger := logger.NewLogger(loggerConfig, logWriter)

	// Database
	dbConfig := &cfg.Database
	dbConn, dbCleanup, err := db.NewDatabase(dbConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Run migrations
	if err := dbConn.RunMigrations(); err != nil {
		dbCleanup()
		return nil, nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	// Storage
	store := storage.NewStore(dbConn.DB)

	// Embedder
	embedder, err := provideEmbedderGen(ctx, cfg, slogLogger)
	if err != nil {
		dbCleanup()
		return nil, nil, fmt.Errorf("failed to create embedder: %w", err)
	}

	// Vector Store
	vectorStore := provideVectorStoreGen(cfg, embedder, slogLogger)

	// Git Client
	gitClient := gitutil.NewClient(slogLogger)

	// Repo Manager
	repoManager := repomanager.New(cfg, store, vectorStore, gitClient, slogLogger)

	// Generator LLM
	generatorLLM, err := provideGeneratorLLMGen(ctx, cfg, slogLogger)
	if err != nil {
		dbCleanup()
		return nil, nil, fmt.Errorf("failed to create generator LLM: %w", err)
	}

	// Parser Registry
	parserRegistry, err := parsers.RegisterLanguagePlugins(slogLogger)
	if err != nil {
		dbCleanup()
		return nil, nil, fmt.Errorf("failed to register parsers: %w", err)
	}

	// Prompt Manager
	promptMgr, err := llm.NewPromptManager()
	if err != nil {
		dbCleanup()
		return nil, nil, fmt.Errorf("failed to create prompt manager: %w", err)
	}

	// RAG Service
	ragService := llm.NewRAGService(cfg, promptMgr, vectorStore, store, generatorLLM, parserRegistry, slogLogger)

	// Review Job
	reviewJob := jobs.NewReviewJob(cfg, ragService, store, repoManager, slogLogger)

	// Dispatcher
	dispatcher := jobs.NewDispatcher(ctx, reviewJob, cfg, slogLogger)

	// Server
	srv := server.NewServer(ctx, cfg, dispatcher, slogLogger)

	// App
	application := app.NewApp(cfg, dbConn, store, vectorStore, repoManager, dispatcher, ragService, srv, gitClient, slogLogger)

	cleanup := func() {
		dbCleanup()
	}

	return application, cleanup, nil
}

func provideVectorStoreGen(cfg *config.Config, embedder embeddings.Embedder, logger *slog.Logger) storage.VectorStore {
	var batchConfig *qdrant.BatchConfig
	if cfg.AI.EmbedderProvider == "gemini" {
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
		batchConfig = &qdrant.BatchConfig{
			BatchSize:               512,
			MaxConcurrency:          8,
			EmbeddingBatchSize:      64,
			EmbeddingMaxConcurrency: 8,
			RetryAttempts:           2,
			RetryDelay:              1 * time.Second,
		}
	}

	return storage.NewQdrantVectorStore(
		cfg,
		logger,
		storage.WithBatchConfig(batchConfig),
		storage.WithInitialEmbedder(cfg.AI.EmbedderModel, embedder),
	)
}

func provideGeneratorLLMGen(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	switch cfg.AI.LLMProvider {
	case "gemini":
		if cfg.AI.GeminiAPIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is not set")
		}
		return gemini.New(ctx, gemini.WithModel(cfg.AI.GeneratorModel), gemini.WithAPIKey(cfg.AI.GeminiAPIKey))
	case "ollama":
		return ollama.New(
			ollama.WithServerURL(cfg.AI.OllamaHost),
			ollama.WithHTTPClient(newOllamaHTTPClientGen()),
			ollama.WithModel(cfg.AI.GeneratorModel),
			ollama.WithLogger(logger),
		)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.AI.LLMProvider)
	}
}

func provideEmbedderGen(ctx context.Context, cfg *config.Config, logger *slog.Logger) (embeddings.Embedder, error) {
	var embedderLLM embeddings.Embedder
	var err error

	switch cfg.AI.EmbedderProvider {
	case "gemini":
		embedderLLM, err = gemini.New(ctx,
			gemini.WithEmbeddingModel(cfg.AI.EmbedderModel),
			gemini.WithAPIKey(cfg.AI.GeminiAPIKey),
		)
	case "ollama":
		embedderLLM, err = ollama.New(
			ollama.WithServerURL(cfg.AI.OllamaHost),
			ollama.WithModel(cfg.AI.EmbedderModel),
			ollama.WithHTTPClient(newOllamaHTTPClientGen()),
			ollama.WithLogger(logger),
		)
	default:
		return nil, fmt.Errorf("unsupported embedder provider: %s", cfg.AI.EmbedderProvider)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create embedder LLM: %w", err)
	}
	return embeddings.NewEmbedder(embedderLLM)
}

func newOllamaHTTPClientGen() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:        100,
			MaxConnsPerHost:     10,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		Timeout: 15 * time.Minute,
	}
}
