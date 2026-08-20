// Package llm contains model construction and review prompt support shared by
// every Code-Warden entry point.
package llm

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/llms/openai"

	"github.com/sevigo/code-warden/internal/config"
)

type generatorBuilder func(context.Context, config.AIConfig, *slog.Logger) (llms.Model, error)

var generatorBuilders = map[string]generatorBuilder{
	"gemini": buildGeminiGenerator,
	"ollama": buildOllamaGenerator,
	"openai": buildOpenAIGenerator,
}

// NewGenerator constructs the configured generator model. All application
// entry points use this factory so provider validation and defaults stay
// consistent between the server, standalone CLI, and evaluation harness.
func NewGenerator(ctx context.Context, cfg config.AIConfig, logger *slog.Logger) (llms.Model, error) {
	if logger == nil {
		logger = slog.Default()
	}

	builder, ok := generatorBuilders[cfg.LLMProvider]
	if !ok {
		return nil, fmt.Errorf("unsupported LLM provider %q (supported: %s)", cfg.LLMProvider, strings.Join(SupportedProviders(), ", "))
	}

	logger.Info("configuring generator model", "provider", cfg.LLMProvider, "model", generatorModelName(cfg))
	return builder(ctx, cfg, logger)
}

// SupportedProviders returns the configured generator provider names in a
// stable order suitable for validation errors and user-facing configuration.
func SupportedProviders() []string {
	providers := make([]string, 0, len(generatorBuilders))
	for name := range generatorBuilders {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers
}

func buildGeminiGenerator(ctx context.Context, cfg config.AIConfig, _ *slog.Logger) (llms.Model, error) {
	if cfg.GeminiAPIKey == "" {
		return nil, fmt.Errorf("ai.gemini_api_key is required for gemini provider")
	}
	return gemini.New(ctx, gemini.WithModel(cfg.GeneratorModel), gemini.WithAPIKey(cfg.GeminiAPIKey))
}

func buildOpenAIGenerator(_ context.Context, cfg config.AIConfig, logger *slog.Logger) (llms.Model, error) {
	if cfg.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("ai.openai_api_key is required for openai provider")
	}

	baseURL := cfg.OpenAIBaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	return openai.New(
		openai.WithAPIKey(cfg.OpenAIAPIKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(generatorModelName(cfg)),
		openai.WithLogger(logger),
	)
}

func buildOllamaGenerator(_ context.Context, cfg config.AIConfig, logger *slog.Logger) (llms.Model, error) {
	headerTimeout := parseProviderDuration("ai.http_response_header_timeout", cfg.HTTPResponseHeaderTimeout, 180*time.Second, logger)
	requestTimeout := parseProviderDuration("ai.http_request_timeout", cfg.HTTPRequestTimeout, 10*time.Minute, logger)

	opts := BuildOllamaOptions(OllamaClientConfig{
		ServerURL:          cfg.OllamaHost,
		APIKey:             cfg.OllamaAPIKey,
		Model:              cfg.GeneratorModel,
		HTTPHeaderTimeout:  headerTimeout,
		HTTPRequestTimeout: requestTimeout,
		ModelKeepAlive:     cfg.ModelKeepAlive,
		EnableThinking:     cfg.EnableThinking,
		ThinkingEffort:     cfg.ThinkingEffort,
		Logger:             logger,
	})
	return ollama.New(opts...)
}

func generatorModelName(cfg config.AIConfig) string {
	if cfg.LLMProvider == "openai" && cfg.OpenAIModel != "" {
		return cfg.OpenAIModel
	}
	return cfg.GeneratorModel
}

func parseProviderDuration(name, value string, fallback time.Duration, logger *slog.Logger) time.Duration {
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		logger.Warn("invalid duration, using fallback", "setting", name, "value", value, "fallback", fallback)
		return fallback
	}
	return duration
}
