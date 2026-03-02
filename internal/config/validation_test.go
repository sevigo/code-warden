package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate_AIConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		wantErr   bool
		errFields []string
	}{
		{
			name: "valid config with ollama provider",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: false,
		},
		{
			name: "valid config with gemini provider",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "gemini",
					GeneratorModel: "gemini-pro",
					EmbedderModel:  "text-embedding-004",
					GeminiAPIKey:   "test-key",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: false,
		},
		{
			name: "empty LLM provider",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"ai.llm_provider"},
		},
		{
			name: "invalid LLM provider",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "invalid-provider",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"ai.llm_provider"},
		},
		{
			name: "empty generator model",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"ai.generator_model"},
		},
		{
			name: "empty embedder model",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"ai.embedder_model"},
		},
		{
			name: "gemini provider without API key",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "gemini",
					GeneratorModel: "gemini-pro",
					EmbedderModel:  "text-embedding-004",
					GeminiAPIKey:   "",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"ai.gemini_api_key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && len(tt.errFields) > 0 {
				errMsg := err.Error()
				for _, field := range tt.errFields {
					if !strings.Contains(errMsg, field) {
						t.Errorf("Validate() error missing expected field %q in error: %v", field, errMsg)
					}
				}
			}
		})
	}
}

func TestValidate_JobsConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		wantErr   bool
		errFields []string
	}{
		{
			name: "valid jobs config",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: false,
		},
		{
			name: "negative max workers",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: -5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"jobs.max_workers"},
		},
		{
			name: "zero max workers",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 0,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"jobs.max_workers"},
		},
		{
			name: "zero queue size",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  0,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"jobs.queue_size"},
		},
		{
			name: "negative queue size",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  -10,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"jobs.queue_size"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && len(tt.errFields) > 0 {
				errMsg := err.Error()
				for _, field := range tt.errFields {
					if !strings.Contains(errMsg, field) {
						t.Errorf("Validate() error missing expected field %q in error: %v", field, errMsg)
					}
				}
			}
		})
	}
}

func TestValidate_DatabaseConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		wantErr   bool
		errFields []string
	}{
		{
			name: "valid database config",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: false,
		},
		{
			name: "empty database host",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"database.host"},
		},
		{
			name: "invalid port - too low",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     0,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"database.port"},
		},
		{
			name: "invalid port - too high",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     99999,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"database.port"},
		},
		{
			name: "empty database name",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"database.database"},
		},
		{
			name: "empty username",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr:   true,
			errFields: []string{"database.username"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && len(tt.errFields) > 0 {
				errMsg := err.Error()
				for _, field := range tt.errFields {
					if !strings.Contains(errMsg, field) {
						t.Errorf("Validate() error missing expected field %q in error: %v", field, errMsg)
					}
				}
			}
		})
	}
}

func TestValidateForServer_GitHubConfig(t *testing.T) {
	tmpDir := t.TempDir()
	validKeyPath := filepath.Join(tmpDir, "valid-key.pem")
	if err := os.WriteFile(validKeyPath, []byte("test-key"), 0600); err != nil {
		t.Fatalf("failed to create test key file: %v", err)
	}

	tests := []struct {
		name      string
		config    *Config
		wantErr   bool
		errFields []string
	}{
		{
			name: "valid github config",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: validKeyPath,
				},
				Agent: AgentConfig{
					Enabled: false,
				},
			},
			wantErr: false,
		},
		{
			name: "missing app id",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          0,
					WebhookSecret:  "secret",
					PrivateKeyPath: validKeyPath,
				},
				Agent: AgentConfig{
					Enabled: false,
				},
			},
			wantErr:   true,
			errFields: []string{"github.app_id"},
		},
		{
			name: "missing webhook secret",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "",
					PrivateKeyPath: validKeyPath,
				},
				Agent: AgentConfig{
					Enabled: false,
				},
			},
			wantErr:   true,
			errFields: []string{"github.webhook_secret"},
		},
		{
			name: "private key file not found",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: "/nonexistent/path/to/key.pem",
				},
				Agent: AgentConfig{
					Enabled: false,
				},
			},
			wantErr:   true,
			errFields: []string{"github private key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForServer()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateForServer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && len(tt.errFields) > 0 {
				errMsg := err.Error()
				for _, field := range tt.errFields {
					if !strings.Contains(errMsg, field) {
						t.Errorf("ValidateForServer() error missing expected field %q in error: %v", field, errMsg)
					}
				}
			}
		})
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	config := &Config{
		AI: AIConfig{
			LLMProvider:    "",
			GeneratorModel: "",
			EmbedderModel:  "",
		},
		Jobs: JobsConfig{
			MaxWorkers: -5,
			QueueSize:  0,
		},
		Database: DBConfig{
			Host:     "",
			Port:     99999,
			Database: "",
			Username: "",
		},
		Storage: StorageConfig{
			QdrantHost: "",
		},
	}

	err := config.Validate()
	if err == nil {
		t.Error("Validate() expected error, got nil")
		return
	}

	errMsg := err.Error()
	expectedFields := []string{
		"ai.llm_provider",
		"ai.generator_model",
		"ai.embedder_model",
		"jobs.max_workers",
		"jobs.queue_size",
		"database.host",
		"database.port",
		"database.database",
		"database.username",
		"storage.qdrant_host",
	}

	for _, field := range expectedFields {
		if !strings.Contains(errMsg, field) {
			t.Errorf("Validate() error missing expected field %q in error: %v", field, errMsg)
		}
	}
}

func TestValidateForCLI(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid CLI config",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama2",
					EmbedderModel:  "nomic-embed-text",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid CLI config - missing ai fields",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "",
					GeneratorModel: "",
					EmbedderModel:  "",
				},
				Jobs: JobsConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "test",
					Username: "postgres",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForCLI()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateForCLI() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateForServer_ValidatesAll(t *testing.T) {
	tmpDir := t.TempDir()
	validKeyPath := filepath.Join(tmpDir, "valid-key.pem")
	if err := os.WriteFile(validKeyPath, []byte("test-key"), 0600); err != nil {
		t.Fatalf("failed to create test key file: %v", err)
	}

	config := &Config{
		AI: AIConfig{
			LLMProvider:    "",
			GeneratorModel: "",
			EmbedderModel:  "",
		},
		Jobs: JobsConfig{
			MaxWorkers: -5,
			QueueSize:  0,
		},
		Database: DBConfig{
			Host:     "",
			Port:     99999,
			Database: "",
			Username: "",
		},
		Storage: StorageConfig{
			QdrantHost: "",
		},
		GitHub: GitHubConfig{
			AppID:          0,
			WebhookSecret:  "",
			PrivateKeyPath: "/nonexistent/key.pem",
		},
		Agent: AgentConfig{
			Enabled: false,
		},
	}

	err := config.ValidateForServer()
	if err == nil {
		t.Error("ValidateForServer() expected error, got nil")
		return
	}

	errMsg := err.Error()
	expectedFields := []string{
		"ai.llm_provider",
		"ai.generator_model",
		"ai.embedder_model",
		"jobs.max_workers",
		"jobs.queue_size",
		"database.host",
		"database.port",
		"database.database",
		"database.username",
		"storage.qdrant_host",
		"github.app_id",
		"github.webhook_secret",
		"github private key",
	}

	for _, field := range expectedFields {
		if !strings.Contains(errMsg, field) {
			t.Errorf("ValidateForServer() error missing expected field %q in error: %v", field, errMsg)
		}
	}
}

func TestAgentConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  AgentConfig
		wantErr bool
	}{
		{
			name:    "disabled agent - no validation",
			config:  AgentConfig{Enabled: false},
			wantErr: false,
		},
		{
			name: "valid enabled agent",
			config: AgentConfig{
				Enabled:               true,
				Provider:              "opencode",
				Model:                 "qwen2.5-coder",
				Timeout:               "30m",
				MaxIterations:         3,
				MaxConcurrentSessions: 2,
				MCPAddr:               "127.0.0.1:8081",
				WorkingDir:            "/tmp/agent",
			},
			wantErr: false,
		},
		{
			name: "invalid provider",
			config: AgentConfig{
				Enabled:  true,
				Provider: "invalid",
				Model:    "qwen2.5-coder",
				Timeout:  "30m",
				MCPAddr:  "127.0.0.1:8081",
			},
			wantErr: true,
		},
		{
			name: "empty model",
			config: AgentConfig{
				Enabled:  true,
				Provider: "opencode",
				Model:    "",
				Timeout:  "30m",
				MCPAddr:  "127.0.0.1:8081",
			},
			wantErr: true,
		},
		{
			name: "invalid timeout",
			config: AgentConfig{
				Enabled:  true,
				Provider: "opencode",
				Model:    "qwen2.5-coder",
				Timeout:  "invalid",
				MCPAddr:  "127.0.0.1:8081",
			},
			wantErr: true,
		},
		{
			name: "zero max iterations",
			config: AgentConfig{
				Enabled:       true,
				Provider:      "opencode",
				Model:         "qwen2.5-coder",
				Timeout:       "30m",
				MaxIterations: 0,
				MCPAddr:       "127.0.0.1:8081",
			},
			wantErr: true,
		},
		{
			name: "empty mcp addr",
			config: AgentConfig{
				Enabled:  true,
				Provider: "opencode",
				Model:    "qwen2.5-coder",
				Timeout:  "30m",
				MCPAddr:  "",
			},
			wantErr: true,
		},
		{
			name: "empty working dir",
			config: AgentConfig{
				Enabled:  true,
				Provider: "opencode",
				Model:    "qwen2.5-coder",
				Timeout:  "30m",
				MCPAddr:  "127.0.0.1:8081",
			},
			wantErr: true,
		},
		{
			name: "path traversal in working dir",
			config: AgentConfig{
				Enabled:    true,
				Provider:   "opencode",
				Model:      "qwen2.5-coder",
				Timeout:    "30m",
				MCPAddr:    "127.0.0.1:8081",
				WorkingDir: "/tmp/../etc",
			},
			wantErr: true,
		},
		{
			name: "relative working dir",
			config: AgentConfig{
				Enabled:    true,
				Provider:   "opencode",
				Model:      "qwen2.5-coder",
				Timeout:    "30m",
				MCPAddr:    "127.0.0.1:8081",
				WorkingDir: "relative/path",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("AgentConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
