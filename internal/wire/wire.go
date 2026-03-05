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
	"github.com/sevigo/code-warden/internal/db"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/globalmcp"
	"github.com/sevigo/code-warden/internal/jobs"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/logger"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/httpclient"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/textsplitter"
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
		rag.NewService,
		provideVectorStore,
		provideGeneratorLLM,
		provideEmbedder,
		provideReranker,
		provideParserRegistry,
		provideTextSplitter,
		provideLoggerConfig,
		provideLogWriter,
		provideDBConfig,
		provideSlogLogger,
		provideSQLXDB,
		provideGlobalMCPServer,
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
			RetryAttempts:           qdrant.DefaultRetryAttempts,
			RetryDelay:              qdrant.DefaultRetryDelay,
			RetryJitter:             qdrant.DefaultRetryJitter,
			MaxRetryDelay:           qdrant.DefaultMaxRetryDelay,
		}
	}

	return storage.NewQdrantVectorStore(
		cfg,
		logger,
		storage.WithBatchConfig(batchConfig),
		storage.WithInitialEmbedder(cfg.AI.EmbedderModel, embedder),
		storage.WithQdrantOptions(
			qdrant.WithTimeout(60*time.Second),
			qdrant.WithKeepaliveTime(15*time.Second),
			qdrant.WithKeepaliveTimeout(5*time.Second),
			qdrant.WithPoolSize(20),
		),
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
		headerTimeout := parseHeaderTimeout(cfg.AI.HTTPResponseHeaderTimeout, logger)
		requestTimeout := parseRequestTimeout(cfg.AI.HTTPRequestTimeout, logger)

		logger.Info("configuring Ollama HTTP client for generator",
			"response_header_timeout", headerTimeout,
			"request_timeout", requestTimeout,
			"model", cfg.AI.GeneratorModel,
		)

		clientCfg := httpclient.NewConfig(
			httpclient.WithResponseHeaderTimeout(headerTimeout),
		)
		// Set overall timeout: use configured value, or 0 (no limit) to rely on ResponseHeaderTimeout
		if requestTimeout > 0 {
			clientCfg.Timeout = requestTimeout
		} else {
			clientCfg.Timeout = 0 // Disable overall timeout, let ResponseHeaderTimeout control
		}

		opts := []ollama.Option{
			ollama.WithServerURL(cfg.AI.OllamaHost),
			ollama.WithAPIKey(cfg.AI.OllamaAPIKey),
			ollama.WithHTTPClient(httpclient.NewClient(clientCfg)),
			ollama.WithModel(cfg.AI.GeneratorModel),
			ollama.WithLogger(logger),
			ollama.WithRetryAttempts(3),
			ollama.WithRetryDelay(2 * time.Second),
		}
		// Add thinking/reasoning mode if enabled
		if cfg.AI.EnableThinking {
			opts = append(opts, ollama.WithThinking(true))
			if cfg.AI.ThinkingEffort != "" {
				opts = append(opts, ollama.WithReasoningEffort(cfg.AI.ThinkingEffort))
			}
		}
		// Add keep_alive for model memory management
		if cfg.AI.ModelKeepAlive != "" {
			opts = append(opts, ollama.WithKeepAlive(cfg.AI.ModelKeepAlive))
		}
		return ollama.New(opts...)
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
		headerTimeout := parseHeaderTimeout(cfg.AI.HTTPResponseHeaderTimeout, logger)
		requestTimeout := parseRequestTimeout(cfg.AI.HTTPRequestTimeout, logger)

		logger.Info("configuring Ollama HTTP client for embedder",
			"response_header_timeout", headerTimeout,
			"request_timeout", requestTimeout,
			"model", cfg.AI.EmbedderModel,
		)

		clientCfg := httpclient.NewConfig(
			httpclient.WithResponseHeaderTimeout(headerTimeout),
		)
		// Set overall timeout: use configured value, or 0 (no limit) to rely on ResponseHeaderTimeout
		if requestTimeout > 0 {
			clientCfg.Timeout = requestTimeout
		} else {
			clientCfg.Timeout = 0 // Disable overall timeout, let ResponseHeaderTimeout control
		}

		opts := []ollama.Option{
			ollama.WithServerURL(cfg.AI.OllamaHost),
			ollama.WithAPIKey(cfg.AI.OllamaAPIKey),
			ollama.WithModel(cfg.AI.EmbedderModel),
			ollama.WithHTTPClient(httpclient.NewClient(clientCfg)),
			ollama.WithLogger(logger),
			ollama.WithRetryAttempts(3),
			ollama.WithRetryDelay(2 * time.Second),
		}
		// Add keep_alive for model memory management
		if cfg.AI.ModelKeepAlive != "" {
			opts = append(opts, ollama.WithKeepAlive(cfg.AI.ModelKeepAlive))
		}
		embedderLLM, err = ollama.New(opts...)
	default:
		return nil, fmt.Errorf("unsupported embedder provider: %s", cfg.AI.EmbedderProvider)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create embedder LLM: %w", err)
	}
	return embeddings.NewEmbedder(embedderLLM)
}

func provideParserRegistry(logger *slog.Logger) (parsers.ParserRegistry, error) {
	return parsers.RegisterLanguagePlugins(logger)
}

func provideTextSplitter(registry parsers.ParserRegistry, model llms.Model, logger *slog.Logger) (textsplitter.TextSplitter, error) {
	tokenizer := llm.NewOllamaTokenizerAdapter(model)
	splitter, err := textsplitter.NewCodeAware(
		registry,
		tokenizer,
		logger,
		textsplitter.WithChunkSize(2000),
		textsplitter.WithChunkOverlap(200),
		textsplitter.WithParentContextConfig(textsplitter.ParentContextConfig{Enabled: true}),
	)
	if err != nil {
		return nil, err
	}
	return splitter, nil
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

func provideGlobalMCPServer(cfg *config.Config, logger *slog.Logger) *globalmcp.Server {
	return globalmcp.NewServer(cfg, logger)
}

func provideReranker(ctx context.Context, cfg *config.Config, logger *slog.Logger, promptMgr *llm.PromptManager) (schema.Reranker, error) {
	if !cfg.AI.EnableReranking {
		logger.Info("Reranking is disabled, using NoOpReranker")
		return schema.NoOpReranker{}, nil
	}

	logger.Info("Initializing LLM Reranker", "model", cfg.AI.RerankerModel)

	headerTimeout := parseHeaderTimeout(cfg.AI.HTTPResponseHeaderTimeout, logger)
	requestTimeout := parseRequestTimeout(cfg.AI.HTTPRequestTimeout, logger)

	logger.Info("configuring Ollama HTTP client for reranker",
		"response_header_timeout", headerTimeout,
		"request_timeout", requestTimeout,
		"model", cfg.AI.RerankerModel,
	)

	clientCfg := httpclient.NewConfig(
		httpclient.WithResponseHeaderTimeout(headerTimeout),
	)
	// Set overall timeout: use configured value, or 0 (no limit) to rely on ResponseHeaderTimeout
	if requestTimeout > 0 {
		clientCfg.Timeout = requestTimeout
	} else {
		clientCfg.Timeout = 0 // Disable overall timeout, let ResponseHeaderTimeout control
	}

	opts := []ollama.Option{
		ollama.WithServerURL(cfg.AI.OllamaHost),
		ollama.WithModel(cfg.AI.RerankerModel),
		ollama.WithHTTPClient(httpclient.NewClient(clientCfg)),
		ollama.WithLogger(logger),
		ollama.WithRetryAttempts(3),
		ollama.WithRetryDelay(2 * time.Second),
	}
	// Add keep_alive for model memory management
	if cfg.AI.ModelKeepAlive != "" {
		opts = append(opts, ollama.WithKeepAlive(cfg.AI.ModelKeepAlive))
	}

	rerankLLM, err := ollama.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create reranker LLM: %w", err)
	}

	const RerankPromptKey = "rerank_precision"

	prompt, err := promptMgr.Render("rerank_precision", nil)
	if err != nil {
		logger.Debug("Loaded rerank prompt", "prompt_len", len(prompt))
	}

	if prompt != "" {
		return llms.NewLLMReranker(rerankLLM, llms.WithConcurrency(3), llms.WithPrompt(prompt)), nil
	}
	return llms.NewLLMReranker(rerankLLM, llms.WithConcurrency(3)), nil
}
