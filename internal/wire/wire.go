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
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/embeddings"
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
		server.NewServerWithStore,
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
		provideWorkspaceRegistry,
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

		logger.Info("configuring Ollama for embedder",
			"response_header_timeout", headerTimeout,
			"request_timeout", requestTimeout,
			"model", cfg.AI.EmbedderModel,
		)

		opts := llm.BuildOllamaOptions(llm.OllamaClientConfig{
			ServerURL:          cfg.AI.OllamaHost,
			APIKey:             cfg.AI.OllamaAPIKey,
			Model:              cfg.AI.EmbedderModel,
			HTTPHeaderTimeout:  headerTimeout,
			HTTPRequestTimeout: requestTimeout,
			ModelKeepAlive:     cfg.AI.ModelKeepAlive,
			Logger:             logger,
		})
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

func provideGlobalMCPServer(ctx context.Context, cfg *config.Config, logger *slog.Logger, registry *globalmcp.WorkspaceRegistry, store storage.Store, vectorStore storage.VectorStore, ragService rag.Service) (*globalmcp.Server, error) {
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

	scopedStore := vectorStore.ForRepo(repo.QdrantCollectionName, cfg.AI.EmbedderModel)

	standaloneCfg := &globalmcp.StandaloneConfig{
		Store:       store,
		VectorStore: scopedStore,
		RAGService:  ragService,
		Repo:        repo,
		RepoConfig:  core.DefaultRepoConfig(),
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

	collectionName := repomanager.GenerateCollectionName(repoFullName)
	repo = &storage.Repository{
		FullName:             repoFullName,
		ClonePath:            repoPath,
		QdrantCollectionName: collectionName,
	}

	if err := store.CreateRepository(ctx, repo); err != nil {
		return nil, fmt.Errorf("failed to create repository record: %w", err)
	}

	return repo, nil
}

func provideWorkspaceRegistry(logger *slog.Logger) *globalmcp.WorkspaceRegistry {
	return globalmcp.NewWorkspaceRegistry(logger)
}

func provideReranker(ctx context.Context, cfg *config.Config, logger *slog.Logger, promptMgr *llm.PromptManager) (schema.Reranker, error) {
	if !cfg.AI.EnableReranking {
		logger.Info("Reranking is disabled, using NoOpReranker")
		return schema.NoOpReranker{}, nil
	}

	logger.Info("Initializing LLM Reranker", "model", cfg.AI.RerankerModel)

	headerTimeout := parseHeaderTimeout(cfg.AI.HTTPResponseHeaderTimeout, logger)
	requestTimeout := parseRequestTimeout(cfg.AI.HTTPRequestTimeout, logger)

	logger.Info("configuring Ollama for reranker",
		"response_header_timeout", headerTimeout,
		"request_timeout", requestTimeout,
		"model", cfg.AI.RerankerModel,
	)

	opts := llm.BuildOllamaOptions(llm.OllamaClientConfig{
		ServerURL:          cfg.AI.OllamaHost,
		Model:              cfg.AI.RerankerModel,
		HTTPHeaderTimeout:  headerTimeout,
		HTTPRequestTimeout: requestTimeout,
		ModelKeepAlive:     cfg.AI.ModelKeepAlive,
		Logger:             logger,
	})

	rerankLLM, err := ollama.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create reranker LLM: %w", err)
	}

	prompt, err := promptMgr.Raw("rerank_precision")
	if err != nil {
		logger.Warn("failed to load rerank prompt, using default", "error", err)
		return llms.NewLLMReranker(rerankLLM, llms.WithConcurrency(3)), nil
	}

	logger.Debug("Loaded rerank prompt", "prompt_len", len(prompt))
	return llms.NewLLMReranker(rerankLLM, llms.WithConcurrency(3), llms.WithPrompt(prompt)), nil
}
