package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/sevigo/code-warden/internal/logger"
)

const (
	llmProviderGemini = "gemini"
)

// Config represents the top-level configuration structure.
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	GitHub   GitHubConfig   `mapstructure:"github"`
	AI       AIConfig       `mapstructure:"ai"`
	Agent    AgentConfig    `mapstructure:"agent"`
	Database DBConfig       `mapstructure:"database"`
	Storage  StorageConfig  `mapstructure:"storage"`
	Logging  logger.Config  `mapstructure:"logging"`
	Features FeaturesConfig `mapstructure:"features"`
}

// AgentConfig holds configuration for the autonomous agent system.
type AgentConfig struct {
	// Enabled determines if agent functionality is active.
	Enabled bool `mapstructure:"enabled"`

	// Provider is the agent provider: "goose" or "opencode".
	Provider string `mapstructure:"provider"`

	// Model is the LLM model to use for the agent.
	Model string `mapstructure:"model"`

	// Timeout is the maximum time for an agent session.
	Timeout string `mapstructure:"timeout"`

	// MaxIterations is the maximum review iterations before escalation.
	MaxIterations int `mapstructure:"max_iterations"`

	// MaxConcurrentSessions is the maximum number of concurrent agent sessions.
	// When reached, new /implement requests will be rejected. Default: 3.
	MaxConcurrentSessions int `mapstructure:"max_concurrent_sessions"`

	// MCPAddr is the address for the MCP server.
	MCPAddr string `mapstructure:"mcp_addr"`

	// OpencodeAddr is the address of the opencode server API (only for opencode provider).
	OpencodeAddr string `mapstructure:"opencode_addr"`

	// WorkingDir is the directory for agent workspaces.
	WorkingDir string `mapstructure:"working_dir"`
}

// GetTimeout parses and returns the timeout duration.
func (c *AgentConfig) GetTimeout() (time.Duration, error) {
	return time.ParseDuration(c.Timeout)
}

// Validate validates the agent configuration.
func (c *AgentConfig) Validate() error {
	if !c.Enabled {
		return nil // No validation needed if disabled
	}

	// Validate provider
	if c.Provider != "opencode" {
		return fmt.Errorf("agent.provider must be 'opencode', got: %s", c.Provider)
	}

	// Validate model is set
	if c.Model == "" {
		return errors.New("agent.model is required when agent is enabled")
	}

	// Validate timeout
	if _, err := c.GetTimeout(); err != nil {
		return fmt.Errorf("agent.timeout is invalid: %w", err)
	}

	// Validate max iterations
	if c.MaxIterations < 1 {
		return errors.New("agent.max_iterations must be >= 1")
	}

	// Validate max concurrent sessions (default to 3 if not set)
	if c.MaxConcurrentSessions < 1 {
		c.MaxConcurrentSessions = 3
	}

	// Validate MCP address
	if c.MCPAddr == "" {
		return errors.New("agent.mcp_addr is required when agent is enabled")
	}

	// Validate working directory
	if c.WorkingDir == "" {
		return errors.New("agent.working_dir is required when agent is enabled")
	}
	// Check for path traversal
	cleanPath := filepath.Clean(c.WorkingDir)
	if strings.Contains(cleanPath, "..") {
		return fmt.Errorf("agent.working_dir contains path traversal: %s", c.WorkingDir)
	}
	// Must be an absolute path
	if !filepath.IsAbs(cleanPath) {
		return fmt.Errorf("agent.working_dir must be an absolute path: %s", c.WorkingDir)
	}

	return nil
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
	OllamaAPIKey         string   `mapstructure:"ollama_api_key"`
	GeminiAPIKey         string   `mapstructure:"gemini_api_key"`
	GeneratorModel       string   `mapstructure:"generator_model"`
	FastModel            string   `mapstructure:"fast_model"`
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
	HyDEConcurrency      int      `mapstructure:"hyde_concurrency"`
	ConsensusTimeout     string   `mapstructure:"consensus_timeout"` // Timeout for individual model reviews in consensus mode (e.g., "5m")
	ConsensusQuorum      float64  `mapstructure:"consensus_quorum"`  // Percentage of models that must finish before synthesis (0.0 to 1.0)

	// Thinking/Reasoning Mode - for models that support it (DeepSeek-R1, Qwen 3, etc.)
	EnableThinking bool   `mapstructure:"enable_thinking"` // Enable thinking/reasoning mode
	ThinkingEffort string `mapstructure:"thinking_effort"` // "low", "medium", "high" (for GPT-OSS models)

	// Model Memory Management
	ModelKeepAlive string `mapstructure:"model_keep_alive"` // How long to keep models loaded (e.g., "10m", "1h", "0" to unload immediately)

	// HTTP Client Overrides
	HTTPResponseHeaderTimeout string `mapstructure:"http_response_header_timeout"` // Timeout for waiting for HTTP response headers (e.g., "30s", "120s")

	// Context Assembly
	ContextTokenBudget int `mapstructure:"context_token_budget"` // Max tokens for RAG context (default: 16000)

	// Review Output Options
	EnableCodeSuggestions bool `mapstructure:"enable_code_suggestions"` // Include code suggestions in review comments (GitHub suggestion blocks)
}

func (c *AIConfig) Validate() error {
	if len(c.ComparisonModels) == 0 {
		return nil
	}
	if c.HyDEConcurrency < 1 {
		return errors.New("ai.hyde_concurrency must be >= 1")
	}
	if c.ConsensusQuorum < 0 || c.ConsensusQuorum > 1 {
		return errors.New("ai.consensus_quorum must be between 0.0 and 1.0")
	}
	if err := c.validateModels(); err != nil {
		return err
	}
	return c.validatePaths()
}

func (c *AIConfig) validateModels() error {
	if len(c.ComparisonModels) > 10 {
		return errors.New("comparison_models cannot exceed 10 to prevent timeout cascades")
	}
	if c.MaxComparisonModels > 10 {
		return errors.New("max_comparison_models cannot exceed 10")
	}

	seenModels := make(map[string]bool)
	for _, m := range c.ComparisonModels {
		if strings.TrimSpace(m) == "" {
			return errors.New("comparison_models cannot contain empty model names")
		}
		if seenModels[m] {
			return fmt.Errorf("duplicate model in comparison_models: %s", m)
		}
		seenModels[m] = true
	}
	return nil
}

func (c *AIConfig) validatePaths() error {
	for _, p := range c.ComparisonPaths {
		if err := validateComparisonPath(p); err != nil {
			return err
		}
	}
	return nil
}

func validateComparisonPath(p string) error {
	clean := filepath.Clean(p)

	// Cross-platform absolute path check
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "\\") {
		return fmt.Errorf("comparison_paths must be relative: %s", p)
	}

	// Traversal check
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("comparison_paths cannot contain traversal components: %s", p)
	}

	// Symlink validation
	return validateSymlink(clean, p)
}

func validateSymlink(clean, original string) error {
	info, err := os.Lstat(clean)
	if err != nil {
		return nil //nolint:nilerr // Path doesn't exist, which is fine for config validation
	}

	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}

	target, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return fmt.Errorf("comparison_paths symlink resolution failed: %s", original)
	}

	if filepath.IsAbs(target) {
		return fmt.Errorf("comparison_paths symlink points to absolute path: %s", original)
	}
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
	v.SetDefault("ai.ollama_api_key", "")
	v.SetDefault("ai.embedder_model", "nomic-embed-text")
	v.SetDefault("ai.embedder_task_description", "search_document")
	v.SetDefault("ai.enable_reranking", false)     // Disabled by default for speed
	v.SetDefault("ai.reranker_model", "gemma2:2b") // Default to a small, fast model
	v.SetDefault("ai.fast_model", "gemma3:1b")     // Very fast model for variation/validation
	v.SetDefault("ai.enable_hybrid_search", true)
	v.SetDefault("ai.sparse_vector_name", "bow_sparse")
	v.SetDefault("ai.enable_hyde", false) // Default to false for performance
	v.SetDefault("ai.hyde_concurrency", 5)
	v.SetDefault("ai.enable_thinking", false)    // Disabled by default - enable per model
	v.SetDefault("ai.thinking_effort", "medium") // "low", "medium", "high"
	v.SetDefault("ai.model_keep_alive", "10m")   // Keep models loaded for 10 minutes
	v.SetDefault("ai.http_response_header_timeout", "120s")
	v.SetDefault("ai.consensus_quorum", 0.66)
	v.SetDefault("ai.context_token_budget", 16000)   // Reasonable default for 128K context models
	v.SetDefault("ai.enable_code_suggestions", true) // Include code suggestions by default

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

	// Agent
	v.SetDefault("agent.enabled", false)
	v.SetDefault("agent.provider", "opencode")
	v.SetDefault("agent.model", "qwen2.5-coder")
	v.SetDefault("agent.timeout", "30m")
	v.SetDefault("agent.max_iterations", 3)
	v.SetDefault("agent.mcp_addr", "127.0.0.1:8081")
	v.SetDefault("agent.opencode_addr", "") // Empty = CLI mode (recommended), non-empty = HTTP API mode
	v.SetDefault("agent.working_dir", "")   // Empty means disabled/no default; must be explicitly set
}

func (c *Config) Validate() error {
	var errs []string

	if err := c.validateAI(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := c.validateServer(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := c.validateDatabase(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := c.validateStorage(); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf("configuration errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) validateAI() error {
	var errs []string

	if c.AI.LLMProvider == "" {
		errs = append(errs, "ai.llm_provider is required")
	} else if c.AI.LLMProvider != "ollama" && c.AI.LLMProvider != llmProviderGemini {
		errs = append(errs, "ai.llm_provider must be 'ollama' or 'gemini'")
	}

	if c.AI.GeneratorModel == "" {
		errs = append(errs, "ai.generator_model is required")
	}

	if c.AI.EmbedderModel == "" {
		errs = append(errs, "ai.embedder_model is required")
	}

	if (c.AI.LLMProvider == llmProviderGemini || c.AI.EmbedderProvider == llmProviderGemini) && c.AI.GeminiAPIKey == "" {
		errs = append(errs, "ai.gemini_api_key is required for gemini provider")
	}

	if err := c.AI.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) validateServer() error {
	if c.Server.MaxWorkers <= 0 {
		return errors.New("server.max_workers must be positive")
	}
	return nil
}

func (c *Config) validateDatabase() error {
	var errs []string

	if c.Database.Host == "" {
		errs = append(errs, "database.host is required")
	}
	if c.Database.Port < 1 || c.Database.Port > 65535 {
		errs = append(errs, "database.port must be between 1 and 65535")
	}
	if c.Database.Database == "" {
		errs = append(errs, "database.database is required")
	}
	if c.Database.Username == "" {
		errs = append(errs, "database.username is required")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) validateStorage() error {
	if c.Storage.QdrantHost == "" {
		return errors.New("storage.qdrant_host is required")
	}
	return nil
}

func (c *Config) validateGitHub() error {
	var errs []string
	if c.GitHub.AppID == 0 {
		errs = append(errs, "github.app_id is required")
	}
	if c.GitHub.WebhookSecret == "" {
		errs = append(errs, "github.webhook_secret is required")
	}
	if _, err := os.Stat(c.GitHub.PrivateKeyPath); os.IsNotExist(err) {
		errs = append(errs, fmt.Sprintf("github private key not found at path: %s", c.GitHub.PrivateKeyPath))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) ValidateForServer() error {
	var errs []string

	if err := c.Validate(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := c.validateGitHub(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := c.Agent.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf("configuration errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) ValidateForCLI() error {
	var errs []string

	if err := c.Validate(); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf("configuration errors: %s", strings.Join(errs, "; "))
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
