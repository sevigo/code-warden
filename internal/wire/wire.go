//go:build wireinject
// +build wireinject

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

	"github.com/google/wire"
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
	"github.com/sevigo/goframe/vectorstores"
	"github.com/sevigo/goframe/vectorstores/qdrant"
)

func InitializeApp(ctx context.Context) (*app.App, func(), error) {
	wire.Build(
		app.NewApp,
		server.NewServer,
		config.LoadConfig,
		db.NewDatabase,
		storage.NewStore,
		repomanager.New,
		gitutil.NewClient,
		jobs.NewDispatcher,
		jobs.NewReviewJob,
		llm.NewPromptManager,
		llm.NewRAGService,
		provideVectorStore,
		provideGeneratorLLM,
		provideEmbedder,
		provideParserRegistry,
		provideLoggerConfig,
		provideLogWriter,
		provideDBConfig,
		provideSlogLogger,
		provideDependencyRetriever,
	)
	return &app.App{}, nil, nil
}

func provideVectorStore(cfg *config.Config, embedder embeddings.Embedder, logger *slog.Logger) storage.VectorStore {
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

func provideGeneratorLLM(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	switch cfg.AI.LLMProvider {
	case "gemini":
		if cfg.AI.GeminiAPIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is not set")
		}
		return gemini.New(ctx, gemini.WithModel(cfg.AI.GeneratorModel), gemini.WithAPIKey(cfg.AI.GeminiAPIKey))
	case "ollama":
		return ollama.New(
			ollama.WithServerURL(cfg.AI.OllamaHost),
			ollama.WithHTTPClient(newOllamaHTTPClient()),
			ollama.WithModel(cfg.AI.GeneratorModel),
			ollama.WithLogger(logger),
		)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.AI.LLMProvider)
	}
}

func provideEmbedder(ctx context.Context, cfg *config.Config, logger *slog.Logger) (embeddings.Embedder, error) {
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
			ollama.WithHTTPClient(newOllamaHTTPClient()),
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

func provideDependencyRetriever(store storage.VectorStore) *vectorstores.DependencyRetriever {
	return vectorstores.NewDependencyRetriever(store)
}

func provideParserRegistry(logger *slog.Logger) (parsers.ParserRegistry, error) {
	return parsers.RegisterLanguagePlugins(logger)
}

func newOllamaHTTPClient() *http.Client {
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
		f, _ := os.OpenFile("code-warden.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		return f
	default:
		return os.Stdout
	}
}

func provideSlogLogger(loggerConfig logger.Config, writer io.Writer) *slog.Logger {
	return logger.NewLogger(loggerConfig, writer)
}
