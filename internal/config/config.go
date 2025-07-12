package config

import (
	"fmt"

	"github.com/spf13/viper"

	"github.com/sevigo/code-warden/internal/logger"
)

// Config holds the application's configuration values.
type Config struct {
	ServerPort           string
	LLMProvider          string
	GeminiAPIKey         string
	LoggerConfig         logger.Config
	GitHubAppID          int64
	GitHubWebhookSecret  string
	GitHubPrivateKeyPath string
	OllamaHost           string
	QdrantHost           string
	GeneratorModelName   string
	EmbedderModelName    string
	MaxWorkers           int
}

// LoadConfig reads configuration from environment variables and a .env file,
// sets sensible defaults, and validates required fields. It uses the Viper
// library to handle configuration loading and precedence.
func LoadConfig() (*Config, error) {
	viper.SetConfigFile(".env")

	viper.SetDefault("SERVER_PORT", "8080")
	viper.SetDefault("LOG_LEVEL", "info")
	viper.SetDefault("LOG_FORMAT", "text")
	viper.SetDefault("LOG_OUTPUT", "stdout")
	viper.SetDefault("OLLAMA_HOST", "http://localhost:11434")
	viper.SetDefault("QDRANT_HOST", "localhost:6334")
	viper.SetDefault("GENERATOR_MODEL_NAME", "gemma3:latest")
	viper.SetDefault("EMBEDDER_MODEL_NAME", "nomic-embed-text")
	viper.SetDefault("MAX_WORKERS", 5)
	viper.SetDefault("GITHUB_PRIVATE_KEY_PATH", "keys/code-warden-app.private-key.pem")
	viper.SetDefault("LLM_PROVIDER", "ollama")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	if viper.GetInt64("GITHUB_APP_ID") == 0 {
		return nil, fmt.Errorf("GITHUB_APP_ID must be set")
	}
	if viper.GetString("GITHUB_WEBHOOK_SECRET") == "" {
		return nil, fmt.Errorf("GITHUB_WEBHOOK_SECRET must be set")
	}

	// Special handling for Gemini generator model name.
	generatorModel := viper.GetString("GENERATOR_MODEL_NAME")
	if viper.GetString("LLM_PROVIDER") == "gemini" {
		geminiModel := viper.GetString("GEMINI_GENERATOR_MODEL_NAME")
		if geminiModel != "" {
			generatorModel = geminiModel
		} else {
			generatorModel = "gemini-2.5-flash"
		}
	}

	return &Config{
		ServerPort: viper.GetString("SERVER_PORT"),
		LoggerConfig: logger.Config{
			Level:  viper.GetString("LOG_LEVEL"),
			Format: viper.GetString("LOG_FORMAT"),
			Output: viper.GetString("LOG_OUTPUT"),
		},
		GitHubAppID:          viper.GetInt64("GITHUB_APP_ID"),
		GitHubWebhookSecret:  viper.GetString("GITHUB_WEBHOOK_SECRET"),
		GitHubPrivateKeyPath: viper.GetString("GITHUB_PRIVATE_KEY_PATH"),
		OllamaHost:           viper.GetString("OLLAMA_HOST"),
		QdrantHost:           viper.GetString("QDRANT_HOST"),
		GeneratorModelName:   generatorModel,
		GeminiAPIKey:         viper.GetString("GEMINI_API_KEY"),
		LLMProvider:          viper.GetString("LLM_PROVIDER"),
		EmbedderModelName:    viper.GetString("EMBEDDER_MODEL_NAME"),
		MaxWorkers:           viper.GetInt("MAX_WORKERS"),
	}, nil
}
