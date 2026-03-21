package llm

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/sevigo/goframe/httpclient"
	"github.com/sevigo/goframe/llms/ollama"
)

// OllamaClientConfig holds configuration for creating Ollama clients.
type OllamaClientConfig struct {
	ServerURL          string
	APIKey             string
	Model              string
	HTTPHeaderTimeout  time.Duration
	HTTPRequestTimeout time.Duration
	ModelKeepAlive     string
	EnableThinking     bool
	ThinkingEffort     string
	EnableReranking    bool
	Logger             *slog.Logger
}

// BuildOllamaOptions creates Ollama client options from configuration.
// This consolidates the common pattern used for generator, embedder, and reranker LLMs.
func BuildOllamaOptions(cfg OllamaClientConfig) []ollama.Option {
	httpClient := buildHTTPClient(cfg.HTTPHeaderTimeout, cfg.HTTPRequestTimeout, cfg.Logger)

	opts := []ollama.Option{
		ollama.WithServerURL(cfg.ServerURL),
		ollama.WithAPIKey(cfg.APIKey),
		ollama.WithModel(cfg.Model),
		ollama.WithHTTPClient(httpClient),
		ollama.WithLogger(cfg.Logger),
		ollama.WithRetryAttempts(3),
		ollama.WithRetryDelay(2 * time.Second),
	}

	if cfg.ModelKeepAlive != "" {
		opts = append(opts, ollama.WithKeepAlive(cfg.ModelKeepAlive))
	}

	if cfg.EnableThinking {
		opts = append(opts, ollama.WithThinking(true))
		if cfg.ThinkingEffort != "" {
			opts = append(opts, ollama.WithReasoningEffort(cfg.ThinkingEffort))
		}
	}

	return opts
}

// buildHTTPClient creates an HTTP client with timeout configuration.
func buildHTTPClient(headerTimeout, requestTimeout time.Duration, logger *slog.Logger) *http.Client {
	if logger != nil && headerTimeout > 0 {
		logger.Debug("configuring Ollama HTTP client",
			"response_header_timeout", headerTimeout,
			"request_timeout", requestTimeout,
		)
	}

	clientCfg := httpclient.NewConfig(
		httpclient.WithResponseHeaderTimeout(headerTimeout),
	)
	if requestTimeout > 0 {
		clientCfg.Timeout = requestTimeout
	} else {
		clientCfg.Timeout = 0 // Disable overall timeout, let ResponseHeaderTimeout control
	}

	return httpclient.NewClient(clientCfg)
}
