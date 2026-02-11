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

func TestValidateAndJoinPath_Symlinks(t *testing.T) {
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

	// Create file outside repo
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create symlink inside repo pointing outside
	symlinkPath := filepath.Join(repoDir, "link_to_outside")
	// Note: Symlinks on Windows require admin or developer mode.
	// If this fails, we might skip the test, but generally CI environments allow it.
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("Skipping symlink test due to error (likely permissions): %v", err)
	}

	service := &ragService{}

	tests := []struct {
		name      string
		relPath   string
		wantErr   bool
		errString string
	}{
		{
			name:      "symlink traversal (link exists points out)",
			relPath:   "link_to_outside/secret.txt",
			wantErr:   true,
			errString: "path traversal",
		},
		{
			name:      "lexical traversal with non-existent path",
			relPath:   "nonexistent/../../outside/secret.txt",
			wantErr:   true,
			errString: "path traversal",
			// This tests the "Pre-resolution" check.
			// Even if "nonexistent" means we can't Resolve, the lexical ".." must catch it.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := service.validateAndJoinPath(repoDir, tt.relPath)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil (val=%q)", val)
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
