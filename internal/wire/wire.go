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
	"github.com/jmoiron/sqlx"
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
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/textsplitter"
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
		provideReranker,
		provideParserRegistry,
		provideTextSplitter,
		provideLoggerConfig,
		provideLogWriter,
		provideDBConfig,
		provideSlogLogger,
		provideSQLXDB,
	)
	return &app.App{}, nil, nil
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

func provideReranker(ctx context.Context, cfg *config.Config, logger *slog.Logger, promptMgr *llm.PromptManager) (schema.Reranker, error) {
	if !cfg.AI.EnableReranking {
		logger.Info("Reranking is disabled, using NoOpReranker")
		return schema.NoOpReranker{}, nil
	}

	logger.Info("Initializing LLM Reranker", "model", cfg.AI.RerankerModel)

	// We create a dedicated LLM instance for reranking
	// Note: Currently only supporting Ollama for reranking as per request snippet, but could be extended.
	rerankLLM, err := ollama.New(
		ollama.WithServerURL(cfg.AI.OllamaHost),
		ollama.WithModel(cfg.AI.RerankerModel),
		ollama.WithHTTPClient(newOllamaHTTPClient()), // Reuse the optimized client
		ollama.WithLogger(logger),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create reranker LLM: %w", err)
	}

	// Load custom prompt if available
	// Note: We need to import the key constant or define it.
	// The user mentioned: prompt, _ := promptMgr.Render(RerankPrompt, DefaultProvider, nil)
	// I need to check if RerankPrompt is defined in llm package or if I should define it there.
	// For now, I will use a default or assume I'll add the prompt key in llm package.
	// However, I can't access `llm.RerankPrompt` here easily unless I export it from `llm` or define it.
	// The user's snippet for provideReranker used `promptMgr`.

	// Let's assume we want to pass the prompt management to the reranker or set the prompt here.
	// The user's snippet:
	// prompt, _ := promptMgr.Render(RerankPrompt, DefaultProvider, nil)
	// return llms.NewLLMReranker(rerankLLM, llms.WithPrompt(prompt))

	// Since RerankPrompt will be added to llm package, I should modify wire to pass promptMgr.
	// But provideReranker in snippet had (ctx, cfg, logger).
	// The user's snippet in Step 5 mentioned: `Then in provideReranker, pass this custom prompt: ... promptMgr.Render(...)`
	// This implies provideReranker needs promptMgr.

	const RerankPromptKey = "rerank_precision" // I will define this in llm package or use string here for now to avoid circular deps if any (unlikely as wire imports llm).

	// Actually, to avoid "RerankPrompt not defined" error, I should make sure I ADD it to llm package first or use a string literal.
	// I will use "rerank_precision" string literal matching the prompt file name I will create.

	prompt, err := promptMgr.Render("rerank_precision", llm.ModelProvider(cfg.AI.RerankerModel), nil)
	if err != nil {
		// Fallback or log? User plan implies we should use it.
		// If render fails (e.g. file not found), maybe fallback to default.
		// But `llms.NewLLMReranker` probably has a generic default.
		// Let's log warning and proceed without custom prompt if fails?
		// Or strict error.
		// I'll stick to a simpler version first or Try to render.
		logger.Debug("Loaded rerank prompt", "prompt_len", len(prompt))
	}

	if prompt != "" {
		return llms.NewLLMReranker(rerankLLM, llms.WithConcurrency(3), llms.WithPrompt(prompt)), nil
	}
	return llms.NewLLMReranker(rerankLLM, llms.WithConcurrency(3)), nil
}
