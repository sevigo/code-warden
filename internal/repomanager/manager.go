package repomanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"log/slog"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/storage"
)

type manager struct {
	cfg         *config.Config
	store       storage.Store
	logger      *slog.Logger
	vectorStore storage.VectorStore
	gitClient   *gitutil.Client
	repoMux     sync.Map
}

//go:generate mockgen -destination=../../mocks/mock_repomanager.go -package=mocks github.com/sevigo/code-warden/internal/repomanager RepoManager
type RepoManager interface {
	SyncRepo(ctx context.Context, event *core.GitHubEvent, token string) (*core.UpdateResult, error)
	GetRepoRecord(ctx context.Context, repoFullName string) (*storage.Repository, error)
	UpdateRepoSHA(ctx context.Context, repoFullName, newSHA string) error
	ScanLocalRepo(ctx context.Context, repoPath, repoFullName string, force bool) (*core.UpdateResult, error)
	GetRepoRecordByPath(ctx context.Context, repoPath string) (*storage.Repository, error)
	LoadRepoConfig(repoPath string) (*core.RepoConfig, error)
	// Clear Locks removes all cached repository locks to free memory.
	ClearLocks()
}

// New creates a manager that implements core.RepoManager.
func New(
	cfg *config.Config,
	store storage.Store,
	vectorStore storage.VectorStore,
	gitClient *gitutil.Client,
	logger *slog.Logger,
) RepoManager {
	return &manager{
		cfg:         cfg,
		store:       store,
		logger:      logger,
		vectorStore: vectorStore,
		gitClient:   gitClient,
	}
}

func (m *manager) SyncRepo(ctx context.Context, ev *core.GitHubEvent, token string) (*core.UpdateResult, error) {
	mu := m.lockFor(ev.RepoFullName)
	mu.Lock()
	defer mu.Unlock()

	// Token resolution order:
	// 1. Caller-provided token (if not placeholder)
	// 2. Config token (if not placeholder)
	// 3. Installation token (if we have InstallationID)
	// 4. Config token fallback (for CLI-added repos)
	// 5. Empty token (will work for public repos)

	// 1. If token is missing/placeholder, try to use config token
	if isPlaceholderToken(token) {
		token = m.cfg.GitHub.Token
	}

	// 2. If token is still placeholder, clear it
	if isPlaceholderToken(token) {
		token = ""
	}

	// 3. If no token but we have an installation ID, fetch a fresh token
	if token == "" && ev.InstallationID > 0 {
		_, instToken, err := github.CreateInstallationClient(ctx, m.cfg, ev.InstallationID, m.logger)
		if err != nil {
			m.logger.Warn("failed to create installation token, falling back to config token",
				"repo", ev.RepoFullName,
				"installation_id", ev.InstallationID,
				"error", err)
		} else {
			token = instToken
		}
	}

	// 4. If still no token, try to find installation ID for this repo via GitHub App
	if token == "" && m.cfg.GitHub.AppID > 0 && m.cfg.GitHub.PrivateKeyPath != "" {
		if instToken := m.tryGetInstallationToken(ctx, ev); instToken != "" {
			token = instToken
		}
	}

	// 5. If still no token (GitHub App not installed), use config token
	if token == "" && m.cfg.GitHub.Token != "" && !isPlaceholderToken(m.cfg.GitHub.Token) {
		token = m.cfg.GitHub.Token
	}

	// 6. If still no token, proceed anyway - public repos don't need authentication
	// Private repos will fail at clone time with a clear error message
	if token == "" {
		m.logger.Info("no token available, proceeding with public clone attempt", "repo", ev.RepoFullName)
	}

	return m.syncRepo(ctx, ev, token)
}

// tryGetInstallationToken attempts to find and create an installation token for the repo.
// Returns empty string if not found or on error.
func (m *manager) tryGetInstallationToken(ctx context.Context, ev *core.GitHubEvent) string {
	installationID, err := github.GetInstallationIDForRepo(ctx, m.cfg, ev.RepoFullName, m.logger)
	if err != nil {
		m.logger.Debug("could not find GitHub App installation for repo", "repo", ev.RepoFullName, "error", err)
		return ""
	}

	ev.InstallationID = installationID
	_, instToken, err := github.CreateInstallationClient(ctx, m.cfg, installationID, m.logger)
	if err != nil {
		m.logger.Warn("failed to create installation token after lookup", "repo", ev.RepoFullName, "error", err)
		return ""
	}

	m.logger.Info("obtained installation token via GitHub App lookup", "repo", ev.RepoFullName, "installation_id", installationID)
	return instToken
}

func isPlaceholderToken(token string) bool {
	return token == "" || strings.HasPrefix(token, "ghp_your_")
}

func (m *manager) ScanLocalRepo(ctx context.Context, repoPath, repoFullName string, force bool) (*core.UpdateResult, error) {
	mu := m.lockFor(repoPath)
	mu.Lock()
	defer mu.Unlock()

	return m.scanLocalRepo(ctx, repoPath, repoFullName, force)
}

func (m *manager) GetRepoRecord(ctx context.Context, repoFullName string) (*storage.Repository, error) {
	return m.store.GetRepositoryByFullName(ctx, repoFullName)
}

func (m *manager) GetRepoRecordByPath(ctx context.Context, repoPath string) (*storage.Repository, error) {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, err
	}
	return m.store.GetRepositoryByClonePath(ctx, absPath)
}

func (m *manager) LoadRepoConfig(repoPath string) (*core.RepoConfig, error) {
	return config.LoadRepoConfig(repoPath)
}

func (m *manager) UpdateRepoSHA(ctx context.Context, repoFullName, newSHA string) error {
	return m.updateRepoSHA(ctx, repoFullName, newSHA)
}

// ClearLocks wipes the internal map of repository-specific mutexes.
// IMPORTANT: This MUST only be called during application shutdown after all
// repository operations have completed. Calling this during active processing
// may lead to lock identity violations (e.g., two goroutines using different
// mutexes for the same repo).
func (m *manager) ClearLocks() {
	m.logger.Info("clearing all repository locks")
	// sync.Map has no Clear() method in Go <= 1.23 — delete keys via Range
	m.repoMux.Range(func(key, _ any) bool {
		m.repoMux.Delete(key)
		return true
	})
}

func (m *manager) lockFor(key string) *sync.Mutex {
	val, _ := m.repoMux.LoadOrStore(key, &sync.Mutex{})
	mux, ok := val.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return mux
}

func (m *manager) updateRepoSHA(ctx context.Context, repoFullName, newSHA string) error {
	repo, err := m.store.GetRepositoryByFullName(ctx, repoFullName)
	if err != nil {
		return fmt.Errorf("query repository for SHA update: %w", err)
	}
	if repo == nil {
		return fmt.Errorf("cannot update SHA for non‑existent repo %s", repoFullName)
	}
	repo.LastIndexedSHA = newSHA
	return m.store.UpdateRepository(ctx, repo)
}

func (m *manager) listRepoFiles(repoPath string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || strings.Contains(path, ".git") {
			return nil
		}
		rel, relErr := filepath.Rel(repoPath, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, strings.ReplaceAll(rel, "\\", "/"))
		return nil
	})
	return files, err
}
