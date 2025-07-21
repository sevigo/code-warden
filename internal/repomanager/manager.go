// File: ./internal/repomanager/manager.go
// Package repomanager handles the persistent cloning and updating of Git repositories.
package repomanager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/utils/merkletrie"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/storage"
)

// manager implements the core.RepoManager interface.
type manager struct {
	cfg     *config.Config
	store   storage.Store
	logger  *slog.Logger
	repoMux sync.Map // To lock operations on a per-repository basis
}

// RepoManager defines the contract for a service that manages local repository clones.
type RepoManager interface {
	// SyncRepo ensures a repository is cloned and up-to-date with the given SHA.
	// It returns the local path and lists of files that have changed since the last indexed SHA.
	SyncRepo(ctx context.Context, event *core.GitHubEvent, token string) (*core.UpdateResult, error)

	// GetRepoRecord retrieves the repository's state from the database.
	GetRepoRecord(ctx context.Context, repoFullName string) (*storage.Repository, error)

	// UpdateRepoSHA updates the last indexed SHA for a repository.
	UpdateRepoSHA(ctx context.Context, repoFullName, newSHA string) error
}

// New creates a new RepoManager.
func New(cfg *config.Config, store storage.Store, logger *slog.Logger) RepoManager {
	return &manager{
		cfg:    cfg,
		store:  store,
		logger: logger,
	}
}

// SyncRepo is the core method that handles cloning or updating a repository.
func (m *manager) SyncRepo(ctx context.Context, event *core.GitHubEvent, token string) (*core.UpdateResult, error) {
	// Use a repository-specific mutex to prevent race conditions from concurrent reviews.
	val, _ := m.repoMux.LoadOrStore(event.RepoFullName, &sync.Mutex{})
	mux := val.(*sync.Mutex)
	mux.Lock()
	defer mux.Unlock()

	repo, err := m.store.GetRepositoryByFullName(ctx, event.RepoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to query repository state: %w", err)
	}

	clonePath := filepath.Join(m.cfg.RepoPath, event.RepoFullName)

	// First time seeing this repository
	if repo == nil {
		return m.handleInitialClone(ctx, event, token, clonePath)
	}

	// Repository already exists, perform an update
	return m.handleIncrementalUpdate(ctx, event, token, repo)
}

// handleInitialClone manages the first-time cloning and indexing of a repository.
func (m *manager) handleInitialClone(ctx context.Context, event *core.GitHubEvent, token, clonePath string) (*core.UpdateResult, error) {
	m.logger.Info("repository not found in DB, performing initial full clone", "repo", event.RepoFullName, "path", clonePath)

	if err := m.cloneRepository(ctx, event.RepoCloneURL, token, clonePath); err != nil {
		return nil, err
	}

	// For the first sync, all files in the repo are considered new.
	allFiles, err := m.listRepoFiles(clonePath)
	if err != nil {
		_ = os.RemoveAll(clonePath) // Clean up on failure
		return nil, fmt.Errorf("failed to list files after initial clone: %w", err)
	}

	collectionName := generateCollectionName(event.RepoFullName, m.cfg.EmbedderModelName)
	newRepo := &storage.Repository{
		FullName:             event.RepoFullName,
		ClonePath:            clonePath,
		QdrantCollectionName: collectionName,
		LastIndexedSHA:       event.HeadSHA, // Start with the current head SHA
	}
	if err := m.store.CreateRepository(ctx, newRepo); err != nil {
		_ = os.RemoveAll(clonePath) // Clean up on failure
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
	m.logger.Info("existing repository found, performing incremental update", "repo", event.RepoFullName, "path", repo.ClonePath)

	gitRepo, err := git.PlainOpen(repo.ClonePath)
	if err != nil {
		// If the path doesn't exist, maybe it was manually deleted. Try re-cloning.
		if errors.Is(err, git.ErrRepositoryNotExists) {
			m.logger.Warn("repository directory not found, attempting re-clone", "path", repo.ClonePath)
			return m.handleInitialClone(ctx, event, token, repo.ClonePath)
		}
		return nil, fmt.Errorf("failed to open repository at %s: %w", repo.ClonePath, err)
	}

	if err := m.fetchUpdates(ctx, gitRepo, token); err != nil {
		return nil, fmt.Errorf("failed to fetch repository updates: %w", err)
	}

	// Checkout the specific commit to ensure the working tree is correct
	worktree, err := gitRepo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(event.HeadSHA), Force: true}); err != nil {
		return nil, fmt.Errorf("failed to checkout commit %s: %w", event.HeadSHA, err)
	}

	added, modified, deleted, err := m.diffCommits(gitRepo, repo.LastIndexedSHA, event.HeadSHA)
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

func (m *manager) cloneRepository(ctx context.Context, repoURL, token, clonePath string) error {
	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := os.MkdirAll(filepath.Dir(clonePath), 0755); err != nil {
		return fmt.Errorf("failed to create repo parent directory: %w", err)
	}
	// Clean up any failed partial clone before starting a new one.
	_ = os.RemoveAll(clonePath)

	authURL, err := url.Parse(repoURL)
	if err != nil {
		return fmt.Errorf("invalid clone URL: %w", err)
	}
	authURL.User = url.UserPassword("x-access-token", token)

	_, err = git.PlainCloneContext(cloneCtx, clonePath, false, &git.CloneOptions{
		URL: authURL.String(),
	})
	return err
}

func (m *manager) fetchUpdates(ctx context.Context, repo *git.Repository, token string) error {
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth: &githttp.BasicAuth{
			Username: "x-access-token",
			Password: token,
		},
		Force: true, // Allow fetching unrelated histories
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return err
	}
	return nil
}

func (m *manager) diffCommits(repo *git.Repository, oldSHA, newSHA string) (added, modified, deleted []string, err error) {
	oldCommit, err := repo.CommitObject(plumbing.NewHash(oldSHA))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("could not find old commit %s: %w", oldSHA, err)
	}
	newCommit, err := repo.CommitObject(plumbing.NewHash(newSHA))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("could not find new commit %s: %w", newSHA, err)
	}

	oldTree, err := oldCommit.Tree()
	if err != nil {
		return nil, nil, nil, err
	}
	newTree, err := newCommit.Tree()
	if err != nil {
		return nil, nil, nil, err
	}

	changes, err := object.DiffTree(oldTree, newTree)
	if err != nil {
		return nil, nil, nil, err
	}

	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			m.logger.Error("failed to get action for git change, skipping", "error", err)
			continue
		}

		// FIX: Use the correct constants from the `merkletrie` package
		switch action {
		case merkletrie.Insert:
			added = append(added, change.To.Name)
		case merkletrie.Modify:
			modified = append(modified, change.To.Name)
		case merkletrie.Delete:
			deleted = append(deleted, change.From.Name)
		}
	}
	return
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
