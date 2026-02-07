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

func TestValidateRepoPath_AdvancedSymlinks(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "scanner-symlink-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	basePath := filepath.Join(tempDir, "repo")
	os.MkdirAll(basePath, 0755)

	outsideDir := filepath.Join(tempDir, "outside")
	os.MkdirAll(outsideDir, 0755)

	s := &Scanner{}

	t.Run("Symlink_pointing_outside", func(t *testing.T) {
		linkPath := filepath.Join(basePath, "out-link")
		err := os.Symlink(outsideDir, linkPath)
		if err != nil {
			t.Skip("Symlinks not supported")
		}
		// provided path is absolute link pointing outside
		_, err = s.validateRepoPath(basePath, linkPath)
		if err == nil {
			t.Error("expected error for symlink pointing outside, got nil")
		}
	})

	t.Run("Symlink_pointing_outside_relative", func(t *testing.T) {
		linkPath := filepath.Join(basePath, "rel-out-link")
		// Link points to ../outside relative to the link location
		err := os.Symlink("../outside", linkPath)
		if err != nil {
			t.Skip("Symlinks not supported")
		}
		_, err = s.validateRepoPath(basePath, "rel-out-link")
		if err == nil {
			t.Error("expected error for relative symlink pointing outside, got nil")
		}
	})

	t.Run("Nested_symlink_pointing_outside", func(t *testing.T) {
		// repo/dir/bad-link -> ../../outside
		subDir := filepath.Join(basePath, "dir")
		os.MkdirAll(subDir, 0755)
		linkPath := filepath.Join(subDir, "bad-link")
		err := os.Symlink("../../outside", linkPath)
		if err != nil {
			t.Skip("Symlinks not supported")
		}
		_, err = s.validateRepoPath(basePath, "dir/bad-link")
		if err == nil {
			t.Error("expected error for nested symlink pointing outside, got nil")
		}
	})

	t.Run("Valid_internal_symlink", func(t *testing.T) {
		// repo/internal-link -> src/main.go
		srcDir := filepath.Join(basePath, "src")
		os.MkdirAll(srcDir, 0755)
		os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main"), 0644)

		linkPath := filepath.Join(basePath, "internal-link")
		err := os.Symlink("src/main.go", linkPath)
		if err != nil {
			t.Skip("Symlinks not supported")
		}

		resolved, err := s.validateRepoPath(basePath, "internal-link")
		if err != nil {
			t.Errorf("unexpected error for internal symlink: %v", err)
		}
		if !strings.HasSuffix(resolved, filepath.FromSlash("src/main.go")) {
			t.Errorf("expected resolved path to end with src/main.go, got %s", resolved)
		}
	})
}
