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
	defer mu.Unlock()

	return m.syncRepo(ctx, ev, token)
}

func (m *manager) ScanLocalRepo(ctx context.Context, repoPath, repoFullName string, force bool) (*core.UpdateResult, error) {
	mu := m.lockFor(repoPath)
	defer mu.Unlock()

	return m.scanLocalRepo(ctx, repoPath, repoFullName, force)
}

func (m *manager) GetRepoRecord(ctx context.Context, repoFullName string) (*storage.Repository, error) {
	return m.store.GetRepositoryByFullName(ctx, repoFullName)
}

func (m *manager) UpdateRepoSHA(ctx context.Context, repoFullName, newSHA string) error {
	return m.updateRepoSHA(ctx, repoFullName, newSHA)
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
