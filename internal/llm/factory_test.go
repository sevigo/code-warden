package llm

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/config"
)

func TestSupportedProviders(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"gemini", "ollama", "openai"}, SupportedProviders())
}

func TestNewGeneratorRejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()

	model, err := NewGenerator(context.Background(), config.AIConfig{LLMProvider: "unknown"}, testLogger())

	require.ErrorContains(t, err, `unsupported LLM provider "unknown"`)
	assert.ErrorContains(t, err, "gemini, ollama, openai")
	assert.Nil(t, model)
}

func TestNewGeneratorValidatesProviderCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		wantErr  string
	}{
		{name: "gemini", provider: "gemini", wantErr: "ai.gemini_api_key is required"},
		{name: "openai", provider: "openai", wantErr: "ai.openai_api_key is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model, err := NewGenerator(context.Background(), config.AIConfig{LLMProvider: tt.provider}, testLogger())
			require.ErrorContains(t, err, tt.wantErr)
			assert.Nil(t, model)
		})
	}
}

func TestNewGeneratorBuildsConfiguredProviders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.AIConfig
	}{
		{
			name: "gemini",
			cfg: config.AIConfig{
				LLMProvider:    "gemini",
				GeminiAPIKey:   "test-key",
				GeneratorModel: "test-model",
			},
		},
		{
			name: "ollama",
			cfg: config.AIConfig{
				LLMProvider:    "ollama",
				OllamaHost:     "http://localhost:11434",
				GeneratorModel: "test-model",
			},
		},
		{
			name: "openai compatible",
			cfg: config.AIConfig{
				LLMProvider:    "openai",
				OpenAIAPIKey:   "test-key",
				OpenAIBaseURL:  "http://localhost:8080/v1",
				OpenAIModel:    "compatible-model",
				GeneratorModel: "fallback-model",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model, err := NewGenerator(context.Background(), tt.cfg, testLogger())
			require.NoError(t, err)
			assert.NotNil(t, model)
		})
	}
}

func TestParseProviderDuration(t *testing.T) {
	t.Parallel()

	fallback := 10 * time.Minute
	assert.Equal(t, 30*time.Second, parseProviderDuration("timeout", "30s", fallback, testLogger()))
	assert.Equal(t, fallback, parseProviderDuration("timeout", "", fallback, testLogger()))
	assert.Equal(t, fallback, parseProviderDuration("timeout", "invalid", fallback, testLogger()))
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
