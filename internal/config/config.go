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
	ServerPort              string
	LLMProvider             string
	EmbedderProvider        string
	GeminiAPIKey            string
	LoggerConfig            logger.Config
	GitHubAppID             int64
	GitHubWebhookSecret     string
	GitHubPrivateKeyPath    string
	OllamaHost              string
	QdrantHost              string
	GeneratorModelName      string
	EmbedderModelName       string
	MaxWorkers              int
	Database                *DBConfig
	RepoPath                string
	GitHubToken             string
	FastAPIServerURL        string `mapstructure:"FASTAPI_SERVER_URL"`
	EmbedderTaskDescription string `mapstructure:"EMBEDDER_TASK_DESCRIPTION"`
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
	v := viper.New()

	setDefaults(v)

	if err := loadEnvFile(v); err != nil {
		return nil, err
	}

	v.AutomaticEnv()

	if err := validateRequired(v); err != nil {
		return nil, err
	}

	dbConfig, err := configureDB(v)
	if err != nil {
		return nil, err
	}

	return &Config{
		ServerPort:              v.GetString("SERVER_PORT"),
		LLMProvider:             v.GetString("LLM_PROVIDER"),
		EmbedderProvider:        v.GetString("EMBEDDER_PROVIDER"),
		GeminiAPIKey:            v.GetString("GEMINI_API_KEY"),
		LoggerConfig:            configureLogger(v),
		GitHubAppID:             v.GetInt64("GITHUB_APP_ID"),
		GitHubWebhookSecret:     v.GetString("GITHUB_WEBHOOK_SECRET"),
		GitHubPrivateKeyPath:    v.GetString("GITHUB_PRIVATE_KEY_PATH"),
		OllamaHost:              v.GetString("OLLAMA_HOST"),
		QdrantHost:              v.GetString("QDRANT_HOST"),
		GeneratorModelName:      getGeneratorModelName(v),
		EmbedderModelName:       getEmbedderModelName(v),
		MaxWorkers:              v.GetInt("MAX_WORKERS"),
		Database:                dbConfig,
		RepoPath:                v.GetString("REPO_PATH"),
		GitHubToken:             v.GetString("GITHUB_TOKEN"),
		FastAPIServerURL:        v.GetString("FASTAPI_SERVER_URL"),
		EmbedderTaskDescription: v.GetString("EMBEDDER_TASK_DESCRIPTION"),
	}, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("SERVER_PORT", "8080")
	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("LOG_FORMAT", "text")
	v.SetDefault("LOG_OUTPUT", "stdout")
	v.SetDefault("OLLAMA_HOST", "http://localhost:11434")
	v.SetDefault("QDRANT_HOST", "localhost:6334")
	v.SetDefault("GENERATOR_MODEL_NAME", "gemma3:latest")
	v.SetDefault("EMBEDDER_MODEL_NAME", "nomic-embed-text") // Ollama default
	v.SetDefault("MAX_WORKERS", 5)
	v.SetDefault("GITHUB_PRIVATE_KEY_PATH", "keys/code-warden-app.private-key.pem")
	v.SetDefault("LLM_PROVIDER", "ollama")
	v.SetDefault("EMBEDDER_PROVIDER", "ollama") // New default
	v.SetDefault("DB_DRIVER", "postgres")
	v.SetDefault("DB_HOST", "localhost")
	v.SetDefault("DB_PORT", 5432)
	v.SetDefault("DB_NAME", "codewarden")
	v.SetDefault("DB_USERNAME", "postgres")
	v.SetDefault("DB_PASSWORD", "password")
	v.SetDefault("DB_SSL_MODE", "disable")
	v.SetDefault("DB_MAX_OPEN_CONNS", 25)
	v.SetDefault("DB_MAX_IDLE_CONNS", 5)
	v.SetDefault("DB_CONN_MAX_LIFETIME", "5m")
	v.SetDefault("DB_CONN_MAX_IDLE_TIME", "5m")
	v.SetDefault("REPO_PATH", "./data/repos")
	v.SetDefault("FASTAPI_SERVER_URL", "http://127.0.0.1:8000")
	v.SetDefault("EMBEDDER_TASK_DESCRIPTION", "Given a web search query, retrieve relevant passages that answer the query")
}

func loadEnvFile(v *viper.Viper) error {
	v.SetConfigFile(".env")
	v.AddConfigPath(".")
	if err := v.MergeInConfig(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to read config file: %w", err)
		}
		slog.Warn("config file .env not found, relying on environment variables and defaults")
	}
	return nil
}

func validateRequired(v *viper.Viper) error {
	if v.GetInt64("GITHUB_APP_ID") == 0 {
		return errors.New("GITHUB_APP_ID must be set")
	}
	if v.GetString("GITHUB_WEBHOOK_SECRET") == "" {
		return errors.New("GITHUB_WEBHOOK_SECRET must be set")
	}
	privateKeyPath := v.GetString("GITHUB_PRIVATE_KEY_PATH")
	if _, err := os.Stat(privateKeyPath); os.IsNotExist(err) {
		return fmt.Errorf("github private key not found at path: %s", privateKeyPath)
	}
	// New: Validate Gemini API key if it's used for either generator or embedder
	if v.GetString("LLM_PROVIDER") == "gemini" || v.GetString("EMBEDDER_PROVIDER") == "gemini" {
		if v.GetString("GEMINI_API_KEY") == "" {
			return errors.New("GEMINI_API_KEY must be set when using the gemini provider for generator or embedder")
		}
	}
	return nil
}

func configureLogger(v *viper.Viper) logger.Config {
	return logger.Config{
		Level:  v.GetString("LOG_LEVEL"),
		Format: v.GetString("LOG_FORMAT"),
		Output: v.GetString("LOG_OUTPUT"),
	}
}

func getGeneratorModelName(v *viper.Viper) string {
	if v.GetString("LLM_PROVIDER") == "gemini" {
		geminiModel := v.GetString("GEMINI_GENERATOR_MODEL_NAME")
		if geminiModel != "" {
			return geminiModel
		}
		return "gemini-2.5-flash"
	}
	return v.GetString("GENERATOR_MODEL_NAME")
}

// New: Dynamically get the embedder model name
func getEmbedderModelName(v *viper.Viper) string {
	if v.GetString("EMBEDDER_PROVIDER") == "gemini" {
		geminiModel := v.GetString("GEMINI_EMBEDDER_MODEL_NAME")
		if geminiModel != "" {
			return geminiModel
		}
		return "gemini-embedding-001"
	}
	return v.GetString("EMBEDDER_MODEL_NAME")
}

func configureDB(v *viper.Viper) (*DBConfig, error) {
	connMaxLifetime, err := time.ParseDuration(v.GetString("DB_CONN_MAX_LIFETIME"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_CONN_MAX_LIFETIME format: %w", err)
	}
	connMaxIdleTime, err := time.ParseDuration(v.GetString("DB_CONN_MAX_IDLE_TIME"))
	if err != nil {
		return nil, fmt.Errorf("invalid DB_CONN_MAX_IDLE_TIME format: %w", err)
	}

	return &DBConfig{
		Driver:          v.GetString("DB_DRIVER"),
		DSN:             getDSN(v),
		Host:            v.GetString("DB_HOST"),
		Port:            v.GetInt("DB_PORT"),
		Database:        v.GetString("DB_NAME"),
		Username:        v.GetString("DB_USERNAME"),
		Password:        v.GetString("DB_PASSWORD"),
		SSLMode:         v.GetString("DB_SSL_MODE"),
		MaxOpenConns:    v.GetInt("DB_MAX_OPEN_CONNS"),
		MaxIdleConns:    v.GetInt("DB_MAX_IDLE_CONNS"),
		ConnMaxLifetime: connMaxLifetime,
		ConnMaxIdleTime: connMaxIdleTime,
	}, nil
}

func getDSN(v *viper.Viper) string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		v.GetString("DB_HOST"),
		v.GetInt("DB_PORT"),
		v.GetString("DB_USERNAME"),
		v.GetString("DB_PASSWORD"),
		v.GetString("DB_NAME"),
		v.GetString("DB_SSL_MODE"),
	)
}
