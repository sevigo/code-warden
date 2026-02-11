package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAndJoinPath_Security(t *testing.T) {
	// Create a temporary directory structure
	// /tmp/repo
	// /tmp/outside
	tmpDir := t.TempDir()

	repoDir := filepath.Join(tmpDir, "repo")
	outsideDir := filepath.Join(tmpDir, "outside")

	if err := os.Mkdir(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}

	service := &ragService{} // Method is on *ragService

	tests := []struct {
		name      string
		relPath   string
		wantErr   bool
		errString string
	}{
		{
			name:    "valid relative path",
			relPath: "internal/main.go",
			wantErr: false,
		},
		{
			name:      "simple traversal attempt",
			relPath:   "../outside/secret.txt",
			wantErr:   true,
			errString: "path traversal",
		},
		{
			name:      "deep traversal attempt",
			relPath:   "internal/../../outside/secret.txt",
			wantErr:   true,
			errString: "path traversal",
		},
		{
			name:    "current directory dot",
			relPath: ".",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.validateAndJoinPath(repoDir, tt.relPath)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errString) {
					t.Errorf("expected error containing %q, got %q", tt.errString, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
