// Package repomanager handles the persistent cloning and updating of Git repositories.
package repomanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/storage"
)

// manager implements the core.RepoManager interface.
type manager struct {
	cfg       *config.Config
	store     storage.Store
	logger    *slog.Logger
	gitClient *gitutil.Client
	repoMux   sync.Map
}

type RepoManager interface {
	SyncRepo(ctx context.Context, event *core.GitHubEvent, token string) (*core.UpdateResult, error)
	GetRepoRecord(ctx context.Context, repoFullName string) (*storage.Repository, error)
	UpdateRepoSHA(ctx context.Context, repoFullName, newSHA string) error
}

// New creates a new RepoManager.
func New(cfg *config.Config, store storage.Store, logger *slog.Logger) RepoManager {
	return &manager{
		cfg:       cfg,
		store:     store,
		logger:    logger,
		gitClient: gitutil.NewClient(logger.With("component", "gitutil")), // Initialize the client
	}
}

// SyncRepo is the core method that handles cloning or updating a repository.
func (m *manager) SyncRepo(ctx context.Context, event *core.GitHubEvent, token string) (*core.UpdateResult, error) {
	val, _ := m.repoMux.LoadOrStore(event.RepoFullName, &sync.Mutex{})
	mux := val.(*sync.Mutex)
	mux.Lock()
	defer mux.Unlock()

	repo, err := m.store.GetRepositoryByFullName(ctx, event.RepoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to query repository state: %w", err)
	}

	clonePath := filepath.Join(m.cfg.RepoPath, event.RepoFullName)

	if repo == nil {
		return m.handleInitialClone(ctx, event, token, clonePath)
	}

	// Use the path from the database for existing repos
	return m.handleIncrementalUpdate(ctx, event, token, repo)
}

// handleInitialClone manages the first-time cloning and indexing of a repository.
func (m *manager) handleInitialClone(ctx context.Context, event *core.GitHubEvent, token, clonePath string) (*core.UpdateResult, error) {
	m.logger.Info("repository not found in DB, performing initial full clone", "repo", event.RepoFullName)

	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Prepare directory and clean up any previous failed attempts
	if err := os.MkdirAll(filepath.Dir(clonePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create repo parent directory: %w", err)
	}
	_ = os.RemoveAll(clonePath)

	// Use the git client to clone
	gitRepo, err := m.gitClient.Clone(cloneCtx, event.RepoCloneURL, clonePath, token)
	if err != nil {
		return nil, err
	}

	// After cloning, checkout the specific commit for this event
	if err := m.gitClient.Checkout(gitRepo, event.HeadSHA); err != nil {
		_ = os.RemoveAll(clonePath)
		return nil, err
	}

	allFiles, err := m.listRepoFiles(clonePath)
	if err != nil {
		_ = os.RemoveAll(clonePath)
		return nil, fmt.Errorf("failed to list files after initial clone: %w", err)
	}

	// Create and save the new repository record
	newRepo := &storage.Repository{
		FullName:             event.RepoFullName,
		ClonePath:            clonePath,
		QdrantCollectionName: generateCollectionName(event.RepoFullName, m.cfg.EmbedderModelName),
		LastIndexedSHA:       event.HeadSHA,
	}
	if err := m.store.CreateRepository(ctx, newRepo); err != nil {
		_ = os.RemoveAll(clonePath)
		return nil, fmt.Errorf("failed to create repository record in DB: %w", err)
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate: allFiles,
		RepoPath:           clonePath,
		IsInitialClone:     true,
	}, nil
}

// handleIncrementalUpdate manages fetching, diffing, and checking out updates.
func (m *manager) handleIncrementalUpdate(ctx context.Context, event *core.GitHubEvent, token string, repo *storage.Repository) (*core.UpdateResult, error) {
	m.logger.Info("existing repository found, performing incremental update", "repo", event.RepoFullName)

	gitRepo, err := m.gitClient.Open(repo.ClonePath)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			m.logger.Warn("repository directory not found, attempting re-clone", "path", repo.ClonePath)
			return m.handleInitialClone(ctx, event, token, repo.ClonePath)
		}
		return nil, err
	}

	// Use the git client for all operations
	if err := m.gitClient.Fetch(ctx, gitRepo, token); err != nil {
		return nil, fmt.Errorf("failed to fetch repository updates: %w", err)
	}

	if err := m.gitClient.Checkout(gitRepo, event.HeadSHA); err != nil {
		return nil, err
	}

	added, modified, deleted, err := m.gitClient.Diff(gitRepo, repo.LastIndexedSHA, event.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("failed to compute diff: %w", err)
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate: append(added, modified...),
		FilesToDelete:      deleted,
		RepoPath:           repo.ClonePath,
		IsInitialClone:     false,
	}, nil
}

// GetRepoRecord retrieves a repository's state from the database.
func (m *manager) GetRepoRecord(ctx context.Context, repoFullName string) (*storage.Repository, error) {
	return m.store.GetRepositoryByFullName(ctx, repoFullName)
}

// UpdateRepoSHA updates the last indexed SHA for a repository after a successful sync.
func (m *manager) UpdateRepoSHA(ctx context.Context, repoFullName, newSHA string) error {
	repo, err := m.store.GetRepositoryByFullName(ctx, repoFullName)
	if err != nil {
		return fmt.Errorf("failed to get repo for SHA update: %w", err)
	}
	if repo == nil {
		return fmt.Errorf("cannot update SHA for non-existent repo: %s", repoFullName)
	}
	repo.LastIndexedSHA = newSHA
	return m.store.UpdateRepository(ctx, repo)
}

func (m *manager) listRepoFiles(repoPath string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.Contains(path, ".git") {
			return nil
		}
		relPath, err := filepath.Rel(repoPath, path)
		if err != nil {
			return err
		}
		files = append(files, relPath)
		return nil
	})
	return files, err
}

func generateCollectionName(repoFullName, embedderName string) string {
	safeRepoName := strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))
	safeEmbedderName := strings.ToLower(strings.Split(embedderName, ":")[0])
	safeRepoName = regexp.MustCompile("[^a-z0-9_-]+").ReplaceAllString(safeRepoName, "")
	safeEmbedderName = regexp.MustCompile("[^a-z0-9_-]+").ReplaceAllString(safeEmbedderName, "")
	collectionName := fmt.Sprintf("repo-%s-%s", safeRepoName, safeEmbedderName)
	if len(collectionName) > 255 {
		collectionName = collectionName[:255]
	}
	return collectionName
}
