package prescan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRepoPath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "scanner-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a real directory to test with
	basePath := filepath.Join(tempDir, "repo")
	err = os.MkdirAll(basePath, 0755)
	if err != nil {
		t.Fatal(err)
	}

	s := &Scanner{}

	tests := []struct {
		name         string
		base         string
		provided     string
		wantErr      bool
		wantContains string
	}{
		{
			name:     "Valid relative path",
			base:     basePath,
			provided: "src/main.go",
			wantErr:  false,
		},
		{
			name:     "Base directory",
			base:     basePath,
			provided: ".",
			wantErr:  false,
		},
		{
			name:         "Path traversal attempt",
			base:         basePath,
			provided:     "../outside.txt",
			wantErr:      true,
			wantContains: "is outside of the repository base path",
		},
		{
			name:         "Absolute path (outside)",
			base:         basePath,
			provided:     filepath.Dir(basePath), // The parent of basePath
			wantErr:      true,
			wantContains: "is outside of the repository base path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.validateRepoPath(tt.base, tt.provided)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRepoPath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
				t.Errorf("validateRepoPath() error = %v, wantContains %v", err, tt.wantContains)
			}
		})
	}
}
