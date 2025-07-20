package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

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
	Database             *DBConfig
}

// DBConfig holds all database connection settings.
type DBConfig struct {
	Driver          string
	DSN             string
	Host            string
	Port            int
	Database        string
	Username        string
	Password        string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// LoadConfig loads configuration from environment variables and .env file.
func LoadConfig() (*Config, error) {
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

	viper.SetDefault("DB_DRIVER", "postgres")
	viper.SetDefault("DB_HOST", "localhost")
	viper.SetDefault("DB_PORT", 5432)
	viper.SetDefault("DB_NAME", "codewarden")
	viper.SetDefault("DB_USERNAME", "postgres")
	viper.SetDefault("DB_PASSWORD", "password")
	viper.SetDefault("DB_SSL_MODE", "disable")
	viper.SetDefault("DB_MAX_OPEN_CONNS", 25)
	viper.SetDefault("DB_MAX_IDLE_CONNS", 5)
	viper.SetDefault("DB_CONN_MAX_LIFETIME", "5m")
	viper.SetDefault("DB_CONN_MAX_IDLE_TIME", "5m")

	viper.SetConfigFile(".env")
	viper.AddConfigPath(".")

	if err := viper.MergeInConfig(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		slog.Warn("config file .env not found, relying on environment variables and defaults")
	}

	viper.AutomaticEnv()

	if viper.GetInt64("GITHUB_APP_ID") == 0 {
		return nil, fmt.Errorf("GITHUB_APP_ID must be set")
	}
	if viper.GetString("GITHUB_WEBHOOK_SECRET") == "" {
		return nil, fmt.Errorf("GITHUB_WEBHOOK_SECRET must be set")
	}

	// Handle Gemini model name separately since it has different defaults
	generatorModel := viper.GetString("GENERATOR_MODEL_NAME")
	if viper.GetString("LLM_PROVIDER") == "gemini" {
		geminiModel := viper.GetString("GEMINI_GENERATOR_MODEL_NAME")
		if geminiModel != "" {
			generatorModel = geminiModel
		} else {
			generatorModel = "gemini-2.5-flash"
		}
	}

	// Create the DSN (Data Source Name) string for Postgres
	dbDSN := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		viper.GetString("DB_HOST"),
		viper.GetInt("DB_PORT"),
		viper.GetString("DB_USERNAME"),
		viper.GetString("DB_PASSWORD"),
		viper.GetString("DB_NAME"),
		viper.GetString("DB_SSL_MODE"),
	)

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
		Database: &DBConfig{
			Driver:          viper.GetString("DB_DRIVER"),
			DSN:             dbDSN,
			Host:            viper.GetString("DB_HOST"),
			Port:            viper.GetInt("DB_PORT"),
			Database:        viper.GetString("DB_NAME"),
			Username:        viper.GetString("DB_USERNAME"),
			Password:        viper.GetString("DB_PASSWORD"),
			SSLMode:         viper.GetString("DB_SSL_MODE"),
			MaxOpenConns:    viper.GetInt("DB_MAX_OPEN_CONNS"),
			MaxIdleConns:    viper.GetInt("DB_MAX_IDLE_CONNS"),
			ConnMaxLifetime: time.Duration(viper.GetInt("DB_CONN_MAX_LIFETIME")) * time.Minute,
			ConnMaxIdleTime: time.Duration(viper.GetInt("DB_CONN_MAX_IDLE_TIME")) * time.Minute,
		},
	}, nil
}
