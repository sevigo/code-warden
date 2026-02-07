package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/sevigo/code-warden/internal/logger"
	"github.com/spf13/viper"
)

const (
	llmProviderGemini = "gemini"
)

// Config represents the top-level configuration structure.
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	GitHub   GitHubConfig   `mapstructure:"github"`
	AI       AIConfig       `mapstructure:"ai"`
	Database DBConfig       `mapstructure:"database"`
	Storage  StorageConfig  `mapstructure:"storage"`
	Logging  logger.Config  `mapstructure:"logging"`
	Features FeaturesConfig `mapstructure:"features"`
}

type ServerConfig struct {
	Port             string `mapstructure:"port"`
	MaxWorkers       int    `mapstructure:"max_workers"`
	FastAPIServerURL string `mapstructure:"fastapi_server_url"`
	SharedSecret     string `mapstructure:"shared_secret"`
	Theme            string `mapstructure:"theme"`
}

type GitHubConfig struct {
	AppID          int64  `mapstructure:"app_id"`
	WebhookSecret  string `mapstructure:"webhook_secret"`
	PrivateKeyPath string `mapstructure:"private_key_path"`
	Token          string `mapstructure:"token"` // For CLI or preload
}

type AIConfig struct {
	LLMProvider          string   `mapstructure:"llm_provider"`
	EmbedderProvider     string   `mapstructure:"embedder_provider"`
	OllamaHost           string   `mapstructure:"ollama_host"`
	GeminiAPIKey         string   `mapstructure:"gemini_api_key"`
	GeneratorModel       string   `mapstructure:"generator_model"`
	EmbedderModel        string   `mapstructure:"embedder_model"`
	EmbedderTask         string   `mapstructure:"embedder_task_description"`
	RerankerModel        string   `mapstructure:"reranker_model"`
	EnableReranking      bool     `mapstructure:"enable_reranking"`
	EnableHybrid         bool     `mapstructure:"enable_hybrid_search"`
	SparseVectorName     string   `mapstructure:"sparse_vector_name"`
	EnableHyDE           bool     `mapstructure:"enable_hyde"` // Hypothetical Document Embeddings (slow but high recall)
	ComparisonModels     []string `mapstructure:"comparison_models"`
	ComparisonPaths      []string `mapstructure:"comparison_paths"`
	MaxConcurrentReviews int      `mapstructure:"max_concurrent_reviews"`
	MaxComparisonModels  int      `mapstructure:"max_comparison_models"`
}

func (c *AIConfig) Validate() error {
	if len(c.ComparisonModels) == 0 {
		return nil
	}
	if len(c.ComparisonModels) > 10 {
		return errors.New("comparison_models cannot exceed 10 to prevent timeout cascades")
	}
	if c.MaxComparisonModels > 10 {
		return errors.New("max_comparison_models cannot exceed 10")
	}

	seen := make(map[string]bool)
	for _, m := range c.ComparisonModels {
		if strings.TrimSpace(m) == "" {
			return errors.New("comparison_models cannot contain empty model names")
		}
		if seen[m] {
			return fmt.Errorf("duplicate model in comparison_models: %s", m)
		}
		seen[m] = true
	}
	// Check if generator model is explicit in comparison models if comparison models are set
	// This is a soft check, just logging a warning might be better, but unexpected behavior if not present.
	// For now, let's keep it strict if we want to force consistency, or lenient.
	// The AI review suggested: "If cfg.GeneratorModel != "" && !seen[cfg.GeneratorModel]" warn.
	// We'll leave it simple for now (uniqueness check).
	return nil
}

type StorageConfig struct {
	QdrantHost string `mapstructure:"qdrant_host"`
	RepoPath   string `mapstructure:"repo_path"`
}

type FeaturesConfig struct {
	EnableBinaryQuantization bool `mapstructure:"enable_binary_quantization"`
	EnableGraphAnalysis      bool `mapstructure:"enable_graph_analysis"`
}

type DBConfig struct {
	Driver          string        `mapstructure:"driver"`
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	Database        string        `mapstructure:"database"`
	Username        string        `mapstructure:"username"`
	Password        string        `mapstructure:"password"`
	SSLMode         string        `mapstructure:"ssl_mode"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`
}

// LoadConfig loads the configuration using Viper with the hierarchy:
// Flags (handled by caller) > Env Vars > Config File > Defaults.
func LoadConfig() (*Config, error) {
	v := viper.New()

	// 1. Set Defaults
	setDefaults(v)

	// 2. Read Config File
	v.SetConfigName("config") // name of config file (without extension)
	v.SetConfigType("yaml")   // REQUIRED if the config file does not have the extension in the name
	v.AddConfigPath(".")      // optionally look for config in the working directory
	v.AddConfigPath("$HOME/.code-warden")

	if err := v.ReadInConfig(); err != nil {
		if !errors.As(err, &viper.ConfigFileNotFoundError{}) {
			// Config file was found but another error occurred (e.g., syntax error)
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		slog.Info("No config file found, using defaults and environment variables")
	} else {
		slog.Info("Loaded configuration", "file", v.ConfigFileUsed())
	}

	// 3. Environment Variables (Automatic mapping)
	// Map env vars like SERVER_PORT to server.port
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 4. Unmarshal
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	// Post-process / construct derived values if needed (e.g., DSN)
	// (Note: DSN construction logic moved to where it's used or handled here if purely config-derived)

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	// Server
	v.SetDefault("server.port", "8080")
	v.SetDefault("server.max_workers", 5)
	v.SetDefault("server.fastapi_server_url", "http://127.0.0.1:8000")

	// GitHub
	v.SetDefault("github.private_key_path", "keys/code-warden-app.private-key.pem")

	// AI
	v.SetDefault("ai.llm_provider", "ollama")
	v.SetDefault("ai.embedder_provider", "ollama")
	v.SetDefault("ai.ollama_host", "http://localhost:11434")
	v.SetDefault("ai.embedder_model", "nomic-embed-text")
	v.SetDefault("ai.embedder_task_description", "search_document")
	v.SetDefault("ai.enable_reranking", false)     // Disabled by default for speed
	v.SetDefault("ai.reranker_model", "gemma2:2b") // Default to a small, fast model
	v.SetDefault("ai.enable_hybrid_search", true)
	v.SetDefault("ai.sparse_vector_name", "bow_sparse")
	v.SetDefault("ai.enable_hyde", false) // Default to false for performance

	// Storage
	v.SetDefault("storage.qdrant_host", "localhost:6334")
	v.SetDefault("storage.repo_path", "./data/repos")

	// Logging
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "text")
	v.SetDefault("logging.output", "stdout")

	// Database
	v.SetDefault("database.driver", "postgres")
	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.database", "codewarden")
	v.SetDefault("database.username", "postgres")
	// Password has no default
	v.SetDefault("database.ssl_mode", "disable")
	v.SetDefault("database.max_open_conns", 25)
	v.SetDefault("database.max_idle_conns", 5)
	v.SetDefault("database.conn_max_lifetime", "5m")
	v.SetDefault("database.conn_max_idle_time", "5m")

	// Features
	v.SetDefault("features.enable_binary_quantization", true)
	v.SetDefault("features.enable_graph_analysis", true)
}

func (c *Config) ValidateForServer() error {
	if c.GitHub.AppID == 0 {
		return errors.New("github.app_id is required")
	}
	if c.GitHub.WebhookSecret == "" {
		return errors.New("github.webhook_secret is required")
	}
	if _, err := os.Stat(c.GitHub.PrivateKeyPath); os.IsNotExist(err) {
		return fmt.Errorf("github private key not found at path: %s", c.GitHub.PrivateKeyPath)
	}
	if (c.AI.LLMProvider == llmProviderGemini || c.AI.EmbedderProvider == llmProviderGemini) && c.AI.GeminiAPIKey == "" {
		return errors.New("ai.gemini_api_key is required for gemini provider")
	}
	if err := c.AI.Validate(); err != nil {
		return fmt.Errorf("ai config invalid: %w", err)
	}
	return nil
}

func (c *Config) ValidateForCLI() error {
	if (c.AI.LLMProvider == llmProviderGemini || c.AI.EmbedderProvider == llmProviderGemini) && c.AI.GeminiAPIKey == "" {
		return errors.New("ai.gemini_api_key is required for gemini provider")
	}
	if err := c.AI.Validate(); err != nil {
		return fmt.Errorf("ai config invalid: %w", err)
	}
	return nil
}

func (db *DBConfig) GetDSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		db.Host,
		db.Port,
		db.Username,
		db.Password,
		db.Database,
		db.SSLMode,
	)
}
