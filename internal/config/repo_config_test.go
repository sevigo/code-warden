package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRepoConfig(t *testing.T) {
	t.Run("valid config file", func(t *testing.T) {
		repoPath := t.TempDir()
		configContent := `
custom_instructions:
  - "Rule 1"
  - "Rule 2"
exclude_dirs:
  - "dist"
  - "build"
exclude_exts:
  - ".txt"
  - "log"
exclude_files:
  - "README.md"
  - "internal/core/repo_config.go"
`
		err := os.WriteFile(filepath.Join(repoPath, ".code-warden.yml"), []byte(configContent), 0644)
		require.NoError(t, err)

		cfg, err := LoadRepoConfig(repoPath)
		require.NoError(t, err)
		assert.Equal(t, []string{"Rule 1", "Rule 2"}, cfg.CustomInstructions)
		assert.Equal(t, []string{"dist", "build"}, cfg.ExcludeDirs)
		assert.Equal(t, []string{".txt", "log"}, cfg.ExcludeExts)
		assert.Equal(t, []string{"README.md", "internal/core/repo_config.go"}, cfg.ExcludeFiles)
	})

	t.Run("missing config file returns defaults", func(t *testing.T) {
		repoPath := t.TempDir()
		cfg, err := LoadRepoConfig(repoPath)
		assert.ErrorIs(t, err, ErrConfigNotFound)
		require.NotNil(t, cfg)
		assert.Empty(t, cfg.CustomInstructions)
		assert.Empty(t, cfg.ExcludeDirs)
		assert.Empty(t, cfg.ExcludeExts)
	})

	t.Run("invalid yaml returns error", func(t *testing.T) {
		repoPath := t.TempDir()
		configContent := "invalid: yaml: content"
		err := os.WriteFile(filepath.Join(repoPath, ".code-warden.yml"), []byte(configContent), 0644)
		require.NoError(t, err)

		cfg, err := LoadRepoConfig(repoPath)
		assert.ErrorIs(t, err, ErrConfigParsing)
		assert.Nil(t, cfg)
	})
}
