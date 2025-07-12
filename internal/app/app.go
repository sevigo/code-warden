// Package app initializes and orchestrates the main components of the Code Warden application.
// It wires together the configuration, server, and other services.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/jobs"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/parsers"
)

// App encapsulates the core components of the application, including the server,
// configuration, and the main context.
type App struct {
	ctx    context.Context
	cfg    *config.Config
	server *server.Server
}

// newOllamaHTTPClient creates a custom http.Client optimized for long-running
// requests to the Ollama server. It includes specific timeouts for dialing,
// TLS handshakes, and the overall request to prevent indefinite hangs.
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

// NewApp initializes and wires together all components of the Code Warden application.
// It sets up the LLM clients, vector store, job dispatcher, and HTTP server based
// on the provided configuration.
func NewApp(ctx context.Context, cfg *config.Config) (*App, error) {
	slog.Info("initializing Code Warden application",
		"ollama_host", cfg.OllamaHost,
		"generator_model", cfg.GeneratorModelName,
		"embedder_model", cfg.EmbedderModelName,
		"max_workers", cfg.MaxWorkers)

	httpClient := newOllamaHTTPClient()

	slog.Info("connecting to generator LLM", "model", cfg.GeneratorModelName)
	generatorLLM, err := createLLM(ctx, cfg)
	if err != nil {
		slog.Error("failed to connect to generator LLM", "error", err)
		return nil, fmt.Errorf("failed to create generator LLM: %w", err)
	}

	slog.Info("connecting to embedder LLM", "model", cfg.EmbedderModelName, "host", cfg.OllamaHost)
	embedderLLM, err := ollama.New(
		ollama.WithServerURL(cfg.OllamaHost),
		ollama.WithModel(cfg.EmbedderModelName),
		ollama.WithHTTPClient(httpClient),
		ollama.WithLogger(slog.Default()),
	)
	if err != nil {
		slog.Error("failed to connect to embedder LLM", "error", err)
		return nil, fmt.Errorf("failed to create embedder LLM: %w", err)
	}

	embedder, err := embeddings.NewEmbedder(embedderLLM)
	if err != nil {
		slog.Error("failed to create embedder service", "error", err)
		return nil, fmt.Errorf("failed to create embedder: %w", err)
	}

	parserRegistry, err := parsers.RegisterLanguagePlugins(slog.Default())
	if err != nil {
		slog.Error("failed to register language parsers", "error", err)
		return nil, fmt.Errorf("failed to register language parsers: %w", err)
	}

	vectorStore := storage.NewQdrantVectorStore(cfg.QdrantHost, embedder, slog.Default())

	slog.Info("initializing RAG service")
	ragService := llm.NewRAGService(vectorStore, generatorLLM, parserRegistry, slog.Default())
	reviewJob := jobs.NewReviewJob(cfg, ragService, slog.Default())
	dispatcher := jobs.NewDispatcher(reviewJob, cfg.MaxWorkers)
	httpServer := server.NewServer(ctx, cfg, dispatcher)

	slog.Info("Code Warden application initialized successfully")
	return &App{
		ctx:    ctx,
		cfg:    cfg,
		server: httpServer,
	}, nil
}

// Start begins the application by starting the HTTP server.
func (a *App) Start() error {
	slog.Info("starting Code Warden",
		"server_port", a.cfg.ServerPort,
		"max_workers", a.cfg.MaxWorkers)

	err := a.server.Start()
	if err != nil {
		slog.Error("failed to start HTTP server", "error", err)
		return err
	}

	return nil
}

// Stop gracefully shuts down the application and its components.
func (a *App) Stop() error {
	slog.Info("shutting down Code Warden gracefully")

	err := a.server.Stop()
	if err != nil {
		slog.Error("error during server shutdown", "error", err)
		return err
	}

	slog.Info("Code Warden stopped successfully")
	return nil
}

// createLLM is a factory function that constructs an LLM client based on the
// provider specified in the application's configuration. It supports multiple
// providers like Gemini and Ollama, abstracting the specific initialization
// logic for each.
func createLLM(ctx context.Context, cfg *config.Config) (llms.Model, error) {
	switch cfg.LLMProvider {
	case "gemini":
		slog.Info("Using Gemini LLM provider", "model", cfg.GeneratorModelName)
		if cfg.GeminiAPIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY is not set in environment for gemini provider")
		}
		return gemini.New(ctx,
			gemini.WithModel(cfg.GeneratorModelName),
			gemini.WithAPIKey(cfg.GeminiAPIKey),
		)

	case "ollama":
		slog.Info("Using Ollama LLM provider", "model", cfg.GeneratorModelName)
		return ollama.New(
			ollama.WithHTTPClient(newOllamaHTTPClient()),
			ollama.WithModel(cfg.GeneratorModelName),
			ollama.WithLogger(slog.Default()),
		)

	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.LLMProvider)
	}
}
