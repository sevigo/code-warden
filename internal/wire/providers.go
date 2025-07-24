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
)

var AppSet = wire.NewSet(
	app.NewApp,
	server.NewServer,
	config.LoadConfig,
	db.NewDatabase,
	storage.NewStore,
	repomanager.New,
	jobs.NewDispatcher,
	jobs.NewReviewJob,
	llm.NewPromptManager,
	llm.NewRAGService,
	storage.NewQdrantVectorStore,
	provideGeneratorLLM,
	provideEmbedder,
	provideParserRegistry,
	provideLoggerConfig,
	provideLogWriter,
	provideDBConfig,
	provideDefaultSlogLogger,
)

func provideGeneratorLLM(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	switch cfg.LLMProvider {
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is not set in environment for gemini provider")
		}
		return gemini.New(ctx,
			gemini.WithModel(cfg.GeneratorModelName),
			gemini.WithAPIKey(cfg.GeminiAPIKey),
		)
	case "ollama":
		return ollama.New(
			ollama.WithHTTPClient(newOllamaHTTPClient()),
			ollama.WithModel(cfg.GeneratorModelName),
			ollama.WithLogger(logger),
		)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.LLMProvider)
	}
}

func provideEmbedder(ctx context.Context, cfg *config.Config, logger *slog.Logger) (embeddings.Embedder, error) {
	logger.Info("connecting to embedder", "provider", cfg.EmbedderProvider, "model", cfg.EmbedderModelName)
	var embedderLLM embeddings.Embedder
	var err error

	switch cfg.EmbedderProvider {
	case "gemini":
		embedderLLM, err = gemini.New(ctx,
			gemini.WithEmbeddingModel(cfg.EmbedderModelName),
			gemini.WithAPIKey(cfg.GeminiAPIKey),
		)
	case "ollama":
		embedderLLM, err = ollama.New(
			ollama.WithServerURL(cfg.OllamaHost),
			ollama.WithModel(cfg.EmbedderModelName),
			ollama.WithHTTPClient(newOllamaHTTPClient()),
			ollama.WithLogger(logger),
		)
	default:
		return nil, fmt.Errorf("unsupported embedder provider: %s", cfg.EmbedderProvider)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create embedder LLM: %w", err)
	}
	return embeddings.NewEmbedder(embedderLLM)
}

func provideParserRegistry(logger *slog.Logger) (parsers.ParserRegistry, error) {
	return parsers.RegisterLanguagePlugins(logger)
}

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
		Timeout:   15 * time.Minute,
	}
}

func provideLoggerConfig(cfg *config.Config) logger.Config {
	return cfg.LoggerConfig
}

func provideLogWriter() io.Writer {
	return os.Stdout
}

func provideDBConfig(cfg *config.Config) *config.DBConfig {
	return cfg.Database
}

func provideDefaultSlogLogger(loggerConfig logger.Config, writer io.Writer) *slog.Logger {
	l := logger.NewLogger(loggerConfig, writer)
	slog.SetDefault(l)
	return l
}
