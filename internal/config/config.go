package config

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/viper"
)

// Config holds the application's configuration values.
type Config struct {
	ServerPort           string
	LLMProvider          string
	GeminiAPIKey         string
	LogLevel             slog.Level
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
	viper.SetDefault("OLLAMA_HOST", "http://localhost:11434")
	viper.SetDefault("QDRANT_HOST", "localhost:6334")
	viper.SetDefault("GENERATOR_MODEL_NAME", "gemma3:latest")
	viper.SetDefault("EMBEDDER_MODEL_NAME", "nomic-embed-text")
	viper.SetDefault("MAX_WORKERS", 5)
	viper.SetDefault("GITHUB_PRIVATE_KEY_PATH", "keys/code-warden-app.private-key.pem")
	viper.SetDefault("LLM_PROVIDER", "ollama")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			slog.Error("failed to read config file", "error", err)
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

	// Parse the log level string into a slog.Level type.
	var logLevel slog.Level
	logLevelStr := strings.ToLower(viper.GetString("LOG_LEVEL"))
	switch logLevelStr {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		slog.Warn("unrecognized log level, defaulting to info", "provided", logLevelStr)
		logLevel = slog.LevelInfo
	}

	return &Config{
		ServerPort:           viper.GetString("SERVER_PORT"),
		LogLevel:             logLevel,
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
