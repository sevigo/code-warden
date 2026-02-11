package config

import (
	"testing"
)

func TestValidateComparisonPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "valid relative path",
			path:    "src/utils",
			wantErr: false,
		},
		{
			name:    "valid single segment",
			path:    "docs",
			wantErr: false,
		},
		{
			name:    "valid nested path",
			path:    "internal/pkg/helpers",
			wantErr: false,
		},
		{
			name:    "traversal with bare ..",
			path:    "..",
			wantErr: true,
		},
		{
			name:    "traversal at start",
			path:    "../secret",
			wantErr: true,
		},
		{
			name:    "absolute unix path",
			path:    "/etc/passwd",
			wantErr: true,
		},
		{
			name:    "backslash prefix treated as absolute",
			path:    "\\windows\\system32",
			wantErr: true,
		},
		{
			name:    "non-existent path is fine",
			path:    "this/does/not/exist/anywhere",
			wantErr: false,
		},
		{
			name:    "current dir is valid",
			path:    ".",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateComparisonPath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateComparisonPath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestValidateModels(t *testing.T) {
	tests := []struct {
		name    string
		config  AIConfig
		wantErr bool
	}{
		{
			name:    "empty models is valid",
			config:  AIConfig{},
			wantErr: false,
		},
		{
			name: "valid unique models",
			config: AIConfig{
				ComparisonModels: []string{"gpt-4", "claude-3"},
			},
			wantErr: false,
		},
		{
			name: "duplicate model name",
			config: AIConfig{
				ComparisonModels: []string{"gpt-4", "claude-3", "gpt-4"},
			},
			wantErr: true,
		},
		{
			name: "empty model name in list",
			config: AIConfig{
				ComparisonModels: []string{"gpt-4", "  ", "claude-3"},
			},
			wantErr: true,
		},
		{
			name: "exceeds max models",
			config: AIConfig{
				ComparisonModels: []string{
					"m1", "m2", "m3", "m4", "m5",
					"m6", "m7", "m8", "m9", "m10", "m11",
				},
			},
			wantErr: true,
		},
		{
			name: "max comparison models exceeded",
			config: AIConfig{
				MaxComparisonModels: 15,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.validateModels()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateModels() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
