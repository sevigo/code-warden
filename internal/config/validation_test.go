package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_ValidateForServer(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					Port:       "8080",
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: false,
		},
		{
			name: "empty LLM provider",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "ai.llm_provider is required",
		},
		{
			name: "invalid LLM provider",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "invalid_provider",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "ai.llm_provider must be 'ollama' or 'gemini'",
		},
		{
			name: "empty generator_model",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "ai.generator_model is required",
		},
		{
			name: "empty embedder_model",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "ai.embedder_model is required",
		},
		{
			name: "zero max_workers",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 0,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "server.max_workers must be greater than 0",
		},
		{
			name: "negative max_workers",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: -5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "server.max_workers must be greater than 0",
		},
		{
			name: "zero queue_size",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  0,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "server.queue_size must be greater than 0",
		},
		{
			name: "negative queue_size",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  -1,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "server.queue_size must be greater than 0",
		},
		{
			name: "empty database host",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "database.host is required",
		},
		{
			name: "invalid database port - too low",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     0,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "database.port must be between 1 and 65535",
		},
		{
			name: "invalid database port - too high",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     99999,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "database.port must be between 1 and 65535",
		},
		{
			name: "missing github app_id",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          0,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKeyFile(t),
				},
			},
			wantErr: true,
			errMsg:  "github.app_id is required",
		},
		{
			name: "missing github private key file",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: "/nonexistent/path/to/key.pem",
				},
			},
			wantErr: true,
			errMsg:  "github private key not found",
		},
		{
			name: "multiple validation errors",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "",
					GeneratorModel: "",
					EmbedderModel:  "",
				},
				Server: ServerConfig{
					MaxWorkers: 0,
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
					PrivateKeyPath: "/nonexistent.pem",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForServer()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.ValidateForServer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && err != nil {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("Config.ValidateForServer() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestConfig_ValidateForCLI(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: false,
		},
		{
			name: "valid config - no github required for CLI",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
				GitHub: GitHubConfig{
					AppID:          0,
					WebhookSecret:  "",
					PrivateKeyPath: "/nonexistent.pem",
				},
			},
			wantErr: false,
		},
		{
			name: "empty LLM provider",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: true,
			errMsg:  "ai.llm_provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForCLI()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.ValidateForCLI() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && err != nil {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("Config.ValidateForCLI() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsgs []string
	}{
		{
			name: "all fields valid",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: false,
		},
		{
			name: "gemini provider requires api key",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "gemini",
					GeneratorModel: "gemini-3-pro",
					EmbedderModel:  "text-embedding-3",
					GeminiAPIKey:   "",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: true,
			errMsgs: []string{"ai.gemini_api_key is required"},
		},
		{
			name: "empty database database name",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: true,
			errMsgs: []string{"database.database is required"},
		},
		{
			name: "empty database username",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "",
				},
				Storage: StorageConfig{
					QdrantHost: "localhost:6334",
				},
			},
			wantErr: true,
			errMsgs: []string{"database.username is required"},
		},
		{
			name: "empty qdrant host",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "llama3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{
					MaxWorkers: 5,
					QueueSize:  100,
				},
				Database: DBConfig{
					Host:     "localhost",
					Port:     5432,
					Database: "testdb",
					Username: "testuser",
				},
				Storage: StorageConfig{
					QdrantHost: "",
				},
			},
			wantErr: true,
			errMsgs: []string{"storage.qdrant_host is required"},
		},
		{
			name: "multiple errors collected",
			config: Config{
				AI: AIConfig{
					LLMProvider:    "invalid",
					GeneratorModel: "",
					EmbedderModel:  "",
				},
				Server: ServerConfig{
					MaxWorkers: -1,
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
			},
			wantErr: true,
			errMsgs: []string{
				"ai.llm_provider",
				"ai.generator_model",
				"ai.embedder_model",
				"server.max_workers",
				"server.queue_size",
				"database.host",
				"database.port",
				"database.database",
				"database.username",
				"storage.qdrant_host",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && len(tt.errMsgs) > 0 && err != nil {
				for _, errMsg := range tt.errMsgs {
					if !contains(err.Error(), errMsg) {
						t.Errorf("Config.Validate() error = %v, want error containing %q", err, errMsg)
					}
				}
			}
		})
	}
}

func TestAgentConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  AgentConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "disabled agent skips validation",
			config: AgentConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "valid enabled agent",
			config: AgentConfig{
				Enabled:               true,
				Provider:              "opencode",
				Model:                 "test-model",
				Timeout:               "30m",
				MaxIterations:         3,
				MaxConcurrentSessions: 3,
				MCPAddr:               "127.0.0.1:8081",
				WorkingDir:            "/tmp/test",
			},
			wantErr: false,
		},
		{
			name: "invalid provider",
			config: AgentConfig{
				Enabled:  true,
				Provider: "invalid_provider",
				Model:    "test-model",
				Timeout:  "30m",
			},
			wantErr: true,
			errMsg:  "agent.provider must be 'opencode'",
		},
		{
			name: "empty model",
			config: AgentConfig{
				Enabled:  true,
				Provider: "opencode",
				Model:    "",
				Timeout:  "30m",
			},
			wantErr: true,
			errMsg:  "agent.model is required",
		},
		{
			name: "invalid timeout",
			config: AgentConfig{
				Enabled:  true,
				Provider: "opencode",
				Model:    "test-model",
				Timeout:  "invalid",
			},
			wantErr: true,
			errMsg:  "agent.timeout is invalid",
		},
		{
			name: "zero max_iterations",
			config: AgentConfig{
				Enabled:       true,
				Provider:      "opencode",
				Model:         "test-model",
				Timeout:       "30m",
				MaxIterations: 0,
			},
			wantErr: true,
			errMsg:  "agent.max_iterations must be >= 1",
		},
		{
			name: "empty mcp_addr",
			config: AgentConfig{
				Enabled:       true,
				Provider:      "opencode",
				Model:         "test-model",
				Timeout:       "30m",
				MaxIterations: 3,
				MCPAddr:       "",
			},
			wantErr: true,
			errMsg:  "agent.mcp_addr is required",
		},
		{
			name: "empty working_dir",
			config: AgentConfig{
				Enabled:       true,
				Provider:      "opencode",
				Model:         "test-model",
				Timeout:       "30m",
				MaxIterations: 3,
				MCPAddr:       "127.0.0.1:8081",
			},
			wantErr: true,
			errMsg:  "agent.working_dir is required",
		},
		{
			name: "path traversal in working_dir",
			config: AgentConfig{
				Enabled:       true,
				Provider:      "opencode",
				Model:         "test-model",
				Timeout:       "30m",
				MaxIterations: 3,
				MCPAddr:       "127.0.0.1:8081",
				WorkingDir:    "../../../etc",
			},
			wantErr: true,
			errMsg:  "agent.working_dir contains path traversal",
		},
		{
			name: "relative path working_dir",
			config: AgentConfig{
				Enabled:       true,
				Provider:      "opencode",
				Model:         "test-model",
				Timeout:       "30m",
				MaxIterations: 3,
				MCPAddr:       "127.0.0.1:8081",
				WorkingDir:    "relative/path",
			},
			wantErr: true,
			errMsg:  "agent.working_dir must be an absolute path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("AgentConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && err != nil {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("AgentConfig.Validate() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsMiddle(s, substr))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func createTempKeyFile(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test-key.pem")
	if err := os.WriteFile(keyPath, []byte("test key content"), 0600); err != nil {
		t.Fatalf("failed to create temp key file: %v", err)
	}
	return keyPath
}
