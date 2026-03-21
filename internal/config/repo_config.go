package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/sevigo/code-warden/internal/core"
)

var (
	ErrConfigNotFound = errors.New("config file not found")
	ErrConfigParsing  = errors.New("config parsing failed")
)

// LoadRepoConfig loads and parses the .code-warden.yml file from a repository path.
func LoadRepoConfig(repoPath string) (*core.RepoConfig, error) {
	configPath := filepath.Join(repoPath, ".code-warden.yml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return core.DefaultRepoConfig(), ErrConfigNotFound
		}
		return nil, fmt.Errorf("failed to read .code-warden.yml: %w", err)
	}

	config := core.DefaultRepoConfig()
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfigParsing, err)
	}
	return config, nil
}

// LoadRepoConfigWithDefaults loads the repo config and returns defaults on error.
// It logs appropriate messages based on whether the config was not found or failed to parse.
func LoadRepoConfigWithDefaults(repoPath, repoFullName string, logger *slog.Logger) *core.RepoConfig {
	repoConfig, err := LoadRepoConfig(repoPath)
	if err == nil {
		return repoConfig
	}

	if errors.Is(err, ErrConfigNotFound) {
		if logger != nil {
			logger.Info("no .code-warden.yml found, using defaults", "repo", repoFullName)
		}
	} else {
		if logger != nil {
			logger.Warn("failed to parse .code-warden.yml, using defaults", "error", err, "repo", repoFullName)
		}
	}
	return core.DefaultRepoConfig()
}
