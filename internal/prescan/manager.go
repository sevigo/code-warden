package prescan

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
)

type Manager struct {
	cfg       *config.Config
	store     storage.Store
	repoMgr   repomanager.RepoManager
	gitClient *gitutil.Client
	logger    *slog.Logger
}

func NewManager(cfg *config.Config, store storage.Store, repoMgr repomanager.RepoManager, gitClient *gitutil.Client, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:       cfg,
		store:     store,
		repoMgr:   repoMgr,
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

	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	ev := &core.GitHubEvent{
		RepoFullName: fmt.Sprintf("%s/%s", owner, repo),
		RepoOwner:    owner,
		RepoName:     repo,
		RepoCloneURL: cloneURL,
	}

	m.logger.Info("Syncing repository via RepoManager", "url", cloneURL)
	updateResult, err := m.repoMgr.SyncRepo(ctx, ev, m.cfg.GitHub.Token)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to sync repo: %w", err)
	}

	return updateResult.RepoPath, owner, repo, nil
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
