package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAIConfig_Validate(t *testing.T) {
	// tempDir := t.TempDir() // Not used in this test but available if needed

	tests := []struct {
		name    string
		config  AIConfig
		wantErr bool
	}{
		{
			name: "Valid config",
			config: AIConfig{
				MaxComparisonModels: 3,
				ComparisonModels:    []string{"gemini-1.5-pro", "deepseek-chat"},
				ComparisonPaths:     []string{"src", "internal/llm"},
			},
			wantErr: false,
		},
		{
			name: "Invalid MaxComparisonModels",
			config: AIConfig{
				MaxComparisonModels: 11,
				ComparisonModels:    []string{"gemini-pro"},
			},
			wantErr: true,
		},
		{
			name: "Duplicate ComparisonModels",
			config: AIConfig{
				MaxComparisonModels: 3,
				ComparisonModels:    []string{"gemini-pro", "gemini-pro"},
			},
			wantErr: true,
		},
		{
			name: "Path traversal in ComparisonPaths",
			config: AIConfig{
				MaxComparisonModels: 3,
				ComparisonModels:    []string{"gemini-pro"},
				ComparisonPaths:     []string{"../outside"},
			},
			wantErr: true,
		},
		{
			name: "Absolute path in ComparisonPaths",
			config: AIConfig{
				MaxComparisonModels: 3,
				ComparisonModels:    []string{"gemini-pro"},
				ComparisonPaths:     []string{"C:/etc/passwd"},
			},
			wantErr: true,
		},
		{
			name: "Traversal with backslashes on Windows",
			config: AIConfig{
				MaxComparisonModels: 3,
				ComparisonModels:    []string{"gemini-pro"},
				ComparisonPaths:     []string{"src\\..\\..\\outside"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.config.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("AIConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAIConfig_Validate_Symlinks(t *testing.T) {
	tempDir := t.TempDir()

	// Create a real directory
	repoDir := filepath.Join(tempDir, "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create an outside directory
	outsideDir := filepath.Join(tempDir, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside repo pointing outside
	linkPath := filepath.Join(repoDir, "bad-link")
	err := os.Symlink(outsideDir, linkPath)
	if err != nil {
		t.Skip("Symlinks not supported on this platform/user")
	}

	config := AIConfig{
		MaxComparisonModels: 3,
		ComparisonModels:    []string{"gemini-pro"},
		ComparisonPaths:     []string{linkPath},
	}

	// This should fail because the symlink points outside or is an absolute path target
	if err := config.Validate(); err == nil {
		t.Error("AIConfig.Validate() expected error for outside symlink, got nil")
	}
}
