package prescan

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/storage"
)

type Manager struct {
	cfg       *config.Config
	store     storage.Store
	gitClient *gitutil.Client
	logger    *slog.Logger
}

func NewManager(cfg *config.Config, store storage.Store, gitClient *gitutil.Client, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:       cfg,
		store:     store,
		gitClient: gitClient,
		logger:    logger,
	}
}

// PrepareRepo resolves the input (URL or Path) -> (LocalPath, Owner, Repo, error)
func (m *Manager) PrepareRepo(ctx context.Context, input string) (string, string, string, error) {
	// 1. Check if it looks like a URL
	if strings.Contains(input, "github.com") || strings.HasPrefix(input, "http") {
		return m.prepareRemoteRepo(ctx, input)
	}
	return m.prepareLocalRepo(input)
}

func (m *Manager) prepareRemoteRepo(ctx context.Context, input string) (string, string, string, error) {
	// Parse URL
	u := input
	if !strings.HasPrefix(u, "http") {
		u = "https://" + u
	}

	owner, repo, _, err := gitutil.ParsePullRequestURL(u)
	if err != nil {
		// Try parsing as simple repo URL if not PR
		s := strings.TrimPrefix(u, "https://")
		s = strings.TrimPrefix(s, "http://")
		parts := strings.Split(s, "/")
		// expect github.com/owner/repo
		if len(parts) >= 3 {
			owner = parts[1]
			repo = strings.TrimSuffix(parts[2], ".git")
		} else {
			return "", "", "", fmt.Errorf("unable to parse repo URL: %s", input)
		}
	}

	// Validate inputs (Path Traversal)
	if strings.Contains(owner, "..") || strings.Contains(repo, "..") ||
		strings.Contains(owner, "\\") || strings.Contains(repo, "\\") {
		return "", "", "", fmt.Errorf("invalid owner or repo name")
	}

	// Target Path
	targetPath := filepath.Join(m.cfg.Storage.RepoPath, owner, repo)

	// Ensure path is within storage directory
	targetPath = filepath.Clean(targetPath)
	if !strings.HasPrefix(targetPath, filepath.Clean(m.cfg.Storage.RepoPath)) {
		return "", "", "", fmt.Errorf("invalid target path: traverses outside storage directory")
	}

	// Check if exists
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		m.logger.Info("Cloning repository", "url", input, "path", targetPath)
		// Ensure parent dir exists (0750 per gosec)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0750); err != nil {
			return "", "", "", fmt.Errorf("failed to create parent dir: %w", err)
		}

		// Reconstruct clean clone URL
		cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

		repoObj, err := m.gitClient.Clone(ctx, cloneURL, targetPath, m.cfg.GitHub.Token)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to clone: %w", err)
		}
		_ = repoObj // might use later?
	} else {
		m.logger.Info("Repository already exists, fetching latest", "path", targetPath)
		// Assuming master/main. We should probably just fetch.
		if err := m.gitClient.Fetch(ctx, targetPath, m.cfg.GitHub.Token); err != nil {
			m.logger.Warn("Fetch failed (continuing anyway)", "error", err)
		}
	}

	return targetPath, owner, repo, nil
}

func (m *Manager) prepareLocalRepo(input string) (string, string, string, error) {
	// 2. Treat as Local Path
	absPath, err := filepath.Abs(input)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid path: %w", err)
	}

	if _, err := os.Stat(absPath); err != nil {
		return "", "", "", fmt.Errorf("path does not exist: %w", err)
	}

	// Try to detect owner/repo from .git config
	// For now, simpler fallback: use folder names? or try opening with gitutil
	// Try to detect owner/repo from .git config
	// For now, simpler fallback: use folder names? or try opening with gitutil
	gitRepo, err := m.gitClient.Open(absPath)
	if err == nil {
		// Try to extract from remote
		remotes, _ := gitRepo.Remotes()
		for _, r := range remotes {
			if len(r.Config().URLs) == 0 {
				continue
			}
			u := r.Config().URLs[0]
			// very naive parsing
			parts := strings.Split(u, "/")
			if len(parts) < 2 {
				continue
			}
			repo := strings.TrimSuffix(parts[len(parts)-1], ".git")
			owner := parts[len(parts)-2]
			// handle git@github.com:owner/repo
			if strings.Contains(owner, ":") {
				owner = strings.Split(owner, ":")[1]
			}
			return absPath, owner, repo, nil
		}
	}

	// Fallback to directory name
	repo := filepath.Base(absPath)
	owner := "local"
	m.logger.Warn("Could not detect git remote, using defaults", "owner", owner, "repo", repo)

	return absPath, owner, repo, nil
}

// GetRepoSHA is a helper to get the HEAD SHA of a repository.
func (m *Manager) GetRepoSHA(ctx context.Context, path string) (string, error) {
	return m.gitClient.GetHeadSHA(ctx, path)
}
