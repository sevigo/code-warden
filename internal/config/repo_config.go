package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sevigo/code-warden/internal/core"
	"gopkg.in/yaml.v3"
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
