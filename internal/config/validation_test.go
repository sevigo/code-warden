package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateForServer_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test.key")
	if err := os.WriteFile(keyPath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		GitHub: GitHubConfig{
			AppID:          12345,
			WebhookSecret:  "secret",
			PrivateKeyPath: keyPath,
		},
		AI: AIConfig{
			LLMProvider:    "ollama",
			GeneratorModel: "gemma3:latest",
			EmbedderModel:  "nomic-embed-text",
		},
		Server: ServerConfig{
			MaxWorkers: 5,
		},
		Database: DBConfig{
			Host: "localhost",
			Port: 5432,
		},
		Agent: AgentConfig{
			Enabled: false,
		},
	}

	if err := cfg.ValidateForServer(); err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}
}

func TestValidateForServer_InvalidGitHubConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "missing app_id",
			config: &Config{
				GitHub: GitHubConfig{
					WebhookSecret:  "secret",
					PrivateKeyPath: "/tmp/key.pem",
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server:   ServerConfig{MaxWorkers: 5},
				Database: DBConfig{Host: "localhost", Port: 5432},
			},
			wantErr: "github.app_id is required",
		},
		{
			name: "missing webhook_secret",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					PrivateKeyPath: "/tmp/key.pem",
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server:   ServerConfig{MaxWorkers: 5},
				Database: DBConfig{Host: "localhost", Port: 5432},
			},
			wantErr: "github.webhook_secret is required",
		},
		{
			name: "missing private key file",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: "/nonexistent/key.pem",
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server:   ServerConfig{MaxWorkers: 5},
				Database: DBConfig{Host: "localhost", Port: 5432},
			},
			wantErr: "github private key not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForServer()
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
			} else if !containsString(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateForServer_InvalidAIConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "invalid llm_provider",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "invalid",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server:   ServerConfig{MaxWorkers: 5},
				Database: DBConfig{Host: "localhost", Port: 5432},
			},
			wantErr: "ai.llm_provider must be 'ollama' or 'gemini'",
		},
		{
			name: "empty generator_model",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "",
					EmbedderModel:  "nomic-embed-text",
				},
				Server:   ServerConfig{MaxWorkers: 5},
				Database: DBConfig{Host: "localhost", Port: 5432},
			},
			wantErr: "ai.generator_model is required",
		},
		{
			name: "empty embedder_model",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "",
				},
				Server:   ServerConfig{MaxWorkers: 5},
				Database: DBConfig{Host: "localhost", Port: 5432},
			},
			wantErr: "ai.embedder_model is required",
		},
		{
			name: "missing gemini_api_key for gemini provider",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "gemini",
					GeneratorModel: "gemini-1.5-flash",
					EmbedderModel:  "text-embedding-gecko",
					GeminiAPIKey:   "",
				},
				Server:   ServerConfig{MaxWorkers: 5},
				Database: DBConfig{Host: "localhost", Port: 5432},
			},
			wantErr: "ai.gemini_api_key is required for gemini provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForServer()
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
			} else if !containsString(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateForServer_InvalidDatabaseConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "missing database host",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{MaxWorkers: 5},
				Database: DBConfig{
					Host: "",
					Port: 5432,
				},
			},
			wantErr: "database.host is required",
		},
		{
			name: "invalid port - zero",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{MaxWorkers: 5},
				Database: DBConfig{
					Host: "localhost",
					Port: 0,
				},
			},
			wantErr: "database.port must be between 1 and 65535",
		},
		{
			name: "invalid port - too large",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{MaxWorkers: 5},
				Database: DBConfig{
					Host: "localhost",
					Port: 99999,
				},
			},
			wantErr: "database.port must be between 1 and 65535",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForServer()
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
			} else if !containsString(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateForServer_InvalidServerConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "zero max_workers",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{MaxWorkers: 0},
				Database: DBConfig{
					Host: "localhost",
					Port: 5432,
				},
			},
			wantErr: "server.max_workers must be positive",
		},
		{
			name: "negative max_workers",
			config: &Config{
				GitHub: GitHubConfig{
					AppID:          12345,
					WebhookSecret:  "secret",
					PrivateKeyPath: createTempKey(t),
				},
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Server: ServerConfig{MaxWorkers: -5},
				Database: DBConfig{
					Host: "localhost",
					Port: 5432,
				},
			},
			wantErr: "server.max_workers must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForServer()
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
			} else if !containsString(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateForServer_MultipleErrors(t *testing.T) {
	cfg := &Config{
		GitHub: GitHubConfig{
			AppID:          0,
			WebhookSecret:  "",
			PrivateKeyPath: "/nonexistent.pem",
		},
		AI: AIConfig{
			LLMProvider:    "invalid",
			GeneratorModel: "",
			EmbedderModel:  "",
		},
		Server: ServerConfig{MaxWorkers: -1},
		Database: DBConfig{
			Host: "",
			Port: -1,
		},
	}

	err := cfg.ValidateForServer()
	if err == nil {
		t.Error("expected multiple validation errors, got nil")
	}

	errStr := err.Error()
	if !containsString(errStr, "github.app_id is required") {
		t.Errorf("expected error to contain 'github.app_id is required', got %q", errStr)
	}
	if !containsString(errStr, "ai.llm_provider must be") {
		t.Errorf("expected error to contain 'ai.llm_provider must be', got %q", errStr)
	}
	if !containsString(errStr, "database.host is required") {
		t.Errorf("expected error to contain 'database.host is required', got %q", errStr)
	}
	if !containsString(errStr, "server.max_workers must be positive") {
		t.Errorf("expected error to contain 'server.max_workers must be positive', got %q", errStr)
	}
}

func TestValidateForCLI_ValidConfig(t *testing.T) {
	cfg := &Config{
		AI: AIConfig{
			LLMProvider:    "ollama",
			GeneratorModel: "gemma3:latest",
			EmbedderModel:  "nomic-embed-text",
		},
		Agent: AgentConfig{
			Enabled: false,
		},
	}

	if err := cfg.ValidateForCLI(); err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}
}

func TestValidateForCLI_InvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "empty llm_provider",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
				Agent: AgentConfig{Enabled: false},
			},
			wantErr: "ai.llm_provider must be 'ollama' or 'gemini'",
		},
		{
			name: "missing generator_model",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "",
					EmbedderModel:  "nomic-embed-text",
				},
				Agent: AgentConfig{Enabled: false},
			},
			wantErr: "ai.generator_model is required",
		},
		{
			name: "missing embedder_model",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "",
				},
				Agent: AgentConfig{Enabled: false},
			},
			wantErr: "ai.embedder_model is required",
		},
		{
			name: "missing gemini_api_key",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "gemini",
					GeneratorModel: "gemini-1.5-flash",
					EmbedderModel:  "text-embedding-gecko",
					GeminiAPIKey:   "",
				},
				Agent: AgentConfig{Enabled: false},
			},
			wantErr: "ai.gemini_api_key is required for gemini provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.ValidateForCLI()
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
			} else if !containsString(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateAI_ProviderValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "ollama provider is valid",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
			},
			wantErr: false,
		},
		{
			name: "Ollama (case insensitive) is valid",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "Ollama",
					GeneratorModel: "gemma3",
					EmbedderModel:  "nomic-embed-text",
				},
			},
			wantErr: false,
		},
		{
			name: "GEMINI (uppercase) is valid",
			config: &Config{
				AI: AIConfig{
					LLMProvider:    "GEMINI",
					GeneratorModel: "gemini-1.5-flash",
					EmbedderModel:  "text-embedding-gecko",
					GeminiAPIKey:   "test-key",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.validateAI()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAI() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDatabase_PortBounds(t *testing.T) {
	tests := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{port: 1, wantErr: false, name: "port 1"},
		{port: 80, wantErr: false, name: "port 80"},
		{port: 443, wantErr: false, name: "port 443"},
		{port: 5432, wantErr: false, name: "port 5432"},
		{port: 65535, wantErr: false, name: "port 65535"},
		{port: 0, wantErr: true, name: "port 0"},
		{port: -1, wantErr: true, name: "port -1"},
		{port: 65536, wantErr: true, name: "port 65536"},
		{port: 99999, wantErr: true, name: "port 99999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Database: DBConfig{
					Host: "localhost",
					Port: tt.port,
				},
			}
			err := cfg.validateDatabase()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDatabase() port=%d error = %v, wantErr %v", tt.port, err, tt.wantErr)
			}
		})
	}
}

func TestValidateServer_Workers(t *testing.T) {
	tests := []struct {
		name       string
		maxWorkers int
		wantErr    bool
	}{
		{maxWorkers: 1, wantErr: false, name: "1 worker"},
		{maxWorkers: 5, wantErr: false, name: "5 workers"},
		{maxWorkers: 100, wantErr: false, name: "100 workers"},
		{maxWorkers: 0, wantErr: true, name: "0 workers"},
		{maxWorkers: -1, wantErr: true, name: "-1 workers"},
		{maxWorkers: -10, wantErr: true, name: "-10 workers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Server: ServerConfig{MaxWorkers: tt.maxWorkers},
			}
			err := cfg.validateServer()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateServer() maxWorkers=%d error = %v, wantErr %v", tt.maxWorkers, err, tt.wantErr)
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && contains(s, substr)
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func createTempKey(t *testing.T) string {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test.key")
	if err := os.WriteFile(keyPath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	return keyPath
}
