// Package reviewcli provides a standalone, GitHub-free entry point for the
// agent-based code review engine. It wires together the LLM provider, prompt
// manager, review runner, and local git diff without requiring the full
// application dependency-injection graph.
package reviewcli

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/llms/openai"

	"github.com/sevigo/code-warden/internal/config"
	llmpkg "github.com/sevigo/code-warden/internal/llm"
)

// parseDurationOpt parses a duration string, returning fallback on error/empty.
func parseDurationOpt(s string, fallback time.Duration, logger *slog.Logger) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		logger.Warn("invalid duration, using fallback", "value", s, "fallback", fallback)
		return fallback
	}
	return d
}

// BuildLLM constructs the generator LLM from configuration. It mirrors the
// provider selection in internal/wire without needing the full DI graph.
// Exported so the eval harness can reuse it.
func BuildLLM(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	return buildLLM(ctx, cfg, logger)
}

// buildLLM constructs the generator LLM from configuration. It mirrors the
// provider selection in internal/wire without needing the full DI graph.
func buildLLM(ctx context.Context, cfg *config.Config, logger *slog.Logger) (llms.Model, error) {
	logger.Info("review: using LLM",
		"provider", cfg.AI.LLMProvider,
		"model", cfg.AI.GeneratorModel,
	)
	switch cfg.AI.LLMProvider {
	case "gemini":
		if cfg.AI.GeminiAPIKey == "" {
			return nil, fmt.Errorf("gemini_api_key is not set")
		}
		return gemini.New(ctx, gemini.WithModel(cfg.AI.GeneratorModel), gemini.WithAPIKey(cfg.AI.GeminiAPIKey))
	case "openai":
		if cfg.AI.OpenAIAPIKey == "" {
			return nil, fmt.Errorf("openai_api_key is not set")
		}
		baseURL := cfg.AI.OpenAIBaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		model := cfg.AI.GeneratorModel
		if cfg.AI.OpenAIModel != "" {
			model = cfg.AI.OpenAIModel
		}
		return openai.New(
			openai.WithAPIKey(cfg.AI.OpenAIAPIKey),
			openai.WithBaseURL(baseURL),
			openai.WithModel(model),
			openai.WithLogger(logger),
		)
	case "ollama":
		headerTimeout := parseDurationOpt(cfg.AI.HTTPResponseHeaderTimeout, 180*time.Second, logger)
		requestTimeout := parseDurationOpt(cfg.AI.HTTPRequestTimeout, 10*time.Minute, logger)
		opts := llmpkg.BuildOllamaOptions(llmpkg.OllamaClientConfig{
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
		return nil, fmt.Errorf("unsupported LLM provider: %q (supported: ollama, gemini, openai)", cfg.AI.LLMProvider)
	}
}
