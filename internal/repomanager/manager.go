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

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/storage"
)

// manager implements the core.RepoManager interface.
type manager struct {
	cfg         *config.Config
	store       storage.Store
	logger      *slog.Logger
	vectorStore storage.VectorStore
	gitClient   *gitutil.Client
	repoMux     sync.Map
}

type RepoManager interface {
	SyncRepo(ctx context.Context, event *core.GitHubEvent, token string) (*core.UpdateResult, error)
	GetRepoRecord(ctx context.Context, repoFullName string) (*storage.Repository, error)
	UpdateRepoSHA(ctx context.Context, repoFullName, newSHA string) error
	ScanLocalRepo(ctx context.Context, repoPath, repoFullName string, force bool) (*core.UpdateResult, error)
}

// New creates a new RepoManager.
// NOTE: You will need to update the wire/providers.go or app.go to pass the vectorStore here.
func New(cfg *config.Config, store storage.Store, vectorStore storage.VectorStore, gitClient *gitutil.Client, logger *slog.Logger) RepoManager {
	return &manager{
		cfg:         cfg,
		store:       store,
		logger:      logger,
		vectorStore: vectorStore,
		gitClient:   gitClient,
	}
}

// SyncRepo is the core method that handles cloning or updating a repository.
func (m *manager) SyncRepo(ctx context.Context, event *core.GitHubEvent, token string) (*core.UpdateResult, error) {
	val, _ := m.repoMux.LoadOrStore(event.RepoFullName, &sync.Mutex{})
	mux, ok := val.(*sync.Mutex)
	if !ok {
		return nil, fmt.Errorf("internal error: failed to assert mutex type")
	}
	mux.Lock()
	defer mux.Unlock()

	repo, err := m.store.GetRepositoryByFullName(ctx, event.RepoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to query repository state: %w", err)
	}

	clonePath := filepath.Join(m.cfg.RepoPath, event.RepoFullName)

	if repo != nil && repo.EmbedderModelName != m.cfg.EmbedderModelName {
		m.logger.Warn("Embedder model changed, forcing full re-index", "repo", event.RepoFullName, "old_model", repo.EmbedderModelName, "new_model", m.cfg.EmbedderModelName)
		if err := m.vectorStore.DeleteCollection(ctx, repo.QdrantCollectionName); err != nil {
			m.logger.Error("failed to delete old qdrant collection during re-index", "collection", repo.QdrantCollectionName, "error", err)
		}
		repo = nil // Treat it as a new clone
	}

	if repo == nil {
		return m.handleInitialClone(ctx, event, token, clonePath)
	}

	return m.handleIncrementalUpdate(ctx, event, token, repo)
}

// handleInitialClone manages the first-time cloning and indexing of a repository.
func (m *manager) handleInitialClone(ctx context.Context, event *core.GitHubEvent, token, clonePath string) (*core.UpdateResult, error) {
	m.logger.Info("repository not found in DB, performing initial full clone", "repo", event.RepoFullName)

	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := os.MkdirAll(filepath.Dir(clonePath), 0750); err != nil {
		return nil, fmt.Errorf("failed to create repo parent directory: %w", err)
	}
	m.cleanupRepoDir(clonePath)

	gitRepo, err := m.gitClient.Clone(cloneCtx, event.RepoCloneURL, clonePath, token)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, err
	}

	if err := m.gitClient.Checkout(gitRepo, event.HeadSHA); err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, err
	}

	allFiles, err := m.listRepoFiles(clonePath)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("failed to list files after initial clone: %w", err)
	}

	newRepo := &storage.Repository{
		FullName:             event.RepoFullName,
		ClonePath:            clonePath,
		QdrantCollectionName: GenerateCollectionName(event.RepoFullName, m.cfg.EmbedderModelName),
		EmbedderModelName:    m.cfg.EmbedderModelName,
		LastIndexedSHA:       "",
	}
	if err := m.store.CreateRepository(ctx, newRepo); err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("failed to create repository record in DB: %w", err)
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate: allFiles,
		RepoPath:           clonePath,
		IsInitialClone:     true,
	}, nil
}

func (m *manager) cleanupRepoDir(path string) {
	if err := os.RemoveAll(path); err != nil {
		m.logger.Warn("failed to clean up repository directory", "path", path, "error", err)
	}
}

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

func (m *manager) GetRepoRecord(ctx context.Context, repoFullName string) (*storage.Repository, error) {
	return m.store.GetRepositoryByFullName(ctx, repoFullName)
}

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

// getRepoFullName is a robust function to determine the 'owner/repo' name.
func (m *manager) getRepoFullName(repo *git.Repository) (string, error) {
	remotes, err := repo.Remotes()
	if err != nil {
		return "", fmt.Errorf("failed to get remotes: %w", err)
	}

	// Iterate through all remotes to find a suitable URL (origin is preferred)
	for _, remote := range remotes {
		if len(remote.Config().URLs) == 0 {
			continue
		}
		repoURL := remote.Config().URLs[0]

		// Try parsing HTTPS URL
		if u, err := url.Parse(repoURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			return strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"), nil
		}

		// Try parsing SSH URL (e.g., git@github.com:owner/repo.git)
		if strings.Contains(repoURL, "@") && strings.Contains(repoURL, ":") {
			if parts := strings.Split(repoURL, ":"); len(parts) > 1 {
				return strings.TrimSuffix(parts[1], ".git"), nil
			}
		}
	}

	return "", errors.New("could not determine repository name from any git remote")
}

func (m *manager) handleLocalFullScan(ctx context.Context, repoPath, repoFullName, headSHA string) (*core.UpdateResult, error) {
	repo, err := m.store.GetRepositoryByFullName(ctx, repoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing repository record: %w", err)
	}

	if repo == nil {
		m.logger.Info("no existing record found, creating new repository record for local scan", "repo", repoFullName)
		newRepo := &storage.Repository{
			FullName:             repoFullName,
			ClonePath:            repoPath,
			QdrantCollectionName: GenerateCollectionName(repoFullName, m.cfg.EmbedderModelName),
			EmbedderModelName:    m.cfg.EmbedderModelName,
			LastIndexedSHA:       "",
		}
		if err := m.store.CreateRepository(ctx, newRepo); err != nil {
			return nil, fmt.Errorf("failed to create repository record in DB: %w", err)
		}
	} else {
		m.logger.Info("existing record found, proceeding with forced full re-scan", "repo", repoFullName)
		repo.ClonePath = repoPath // Ensure the clone path is up-to-date
		if err := m.store.UpdateRepository(ctx, repo); err != nil {
			return nil, fmt.Errorf("failed to update repository clone path: %w", err)
		}
	}

	filesToAddOrUpdate, err := m.listRepoFiles(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to list files in local repository: %w", err)
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate: filesToAddOrUpdate,
		RepoPath:           repoPath,
		RepoFullName:       repoFullName,
		HeadSHA:            headSHA,
		IsInitialClone:     true,
	}, nil
}

func (m *manager) handleLocalIncrementalScan(ctx context.Context, gitRepo *git.Repository, repoRecord *storage.Repository, repoPath, headSHA string) (*core.UpdateResult, error) {
	m.logger.Info("existing repository found, performing incremental update", "repo", repoRecord.FullName)

	lastIndexedSHA := repoRecord.LastIndexedSHA
	if lastIndexedSHA == headSHA {
		m.logger.Info("SHAs are identical, no changes to diff.")
		return &core.UpdateResult{
			FilesToAddOrUpdate: []string{},
			FilesToDelete:      []string{},
			RepoPath:           repoPath,
			RepoFullName:       repoRecord.FullName,
			HeadSHA:            headSHA,
			IsInitialClone:     false,
		}, nil
	}

	m.logger.Info("Comparing SHAs for diff", "last_indexed_sha", lastIndexedSHA, "current_head_sha", headSHA)
	added, modified, deleted, err := m.gitClient.Diff(gitRepo, lastIndexedSHA, headSHA)
	if err != nil {
		m.logger.Warn("failed to compute diff, falling back to full scan", "error", err)
		return m.handleLocalFullScan(ctx, repoPath, repoRecord.FullName, headSHA)
	}

	m.logger.Info("Local scan diff result", "added", len(added), "modified", len(modified), "deleted", len(deleted))
	return &core.UpdateResult{
		FilesToAddOrUpdate: append(added, modified...),
		FilesToDelete:      deleted,
		RepoPath:           repoPath,
		RepoFullName:       repoRecord.FullName,
		HeadSHA:            headSHA,
		IsInitialClone:     false,
	}, nil
}

// ScanLocalRepo scans a local git repository, either fully or incrementally.
func (m *manager) ScanLocalRepo(ctx context.Context, repoPath, repoFullName string, force bool) (*core.UpdateResult, error) {
	m.logger.Info("scanning local repository", "path", repoPath)

	gitRepo, err := m.gitClient.Open(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open local git repository at '%s': %w", repoPath, err)
	}

	head, err := gitRepo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD commit: %w", err)
	}
	headSHA := head.Hash().String()

	if repoFullName == "" {
		repoRecordByPath, err := m.store.GetRepositoryByClonePath(ctx, repoPath)
		if err != nil {
			return nil, fmt.Errorf("failed to query for existing repository by path: %w", err)
		}

		if repoRecordByPath != nil {
			// We found a manually registered repo! Use its name.
			m.logger.Info("Found registered repository matching path", "path", repoPath, "repo_name", repoRecordByPath.FullName)
			repoFullName = repoRecordByPath.FullName
		} else {
			// Only if no registered path is found, fall back to guessing the name.
			var nameErr error
			repoFullName, nameErr = m.getRepoFullName(gitRepo)
			if nameErr != nil {
				return nil, fmt.Errorf("failed to automatically determine repo name: %w. Please use the --repo-full-name flag (e.g., --repo-full-name 'owner/repo')", nameErr)
			}
		}
	}

	if force {
		return m.handleLocalFullScan(ctx, repoPath, repoFullName, headSHA)
	}

	repoRecord, err := m.store.GetRepositoryByFullName(ctx, repoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to query repository state: %w", err)
	}

	// This is the check for embedder model change for local scans.
	if repoRecord != nil && repoRecord.EmbedderModelName != m.cfg.EmbedderModelName {
		m.logger.Warn("Embedder model changed for local repo, forcing full re-scan", "repo", repoFullName, "old_model", repoRecord.EmbedderModelName, "new_model", m.cfg.EmbedderModelName)
		if err := m.vectorStore.DeleteCollection(ctx, repoRecord.QdrantCollectionName); err != nil {
			m.logger.Error("failed to delete old qdrant collection during re-scan", "collection", repoRecord.QdrantCollectionName, "error", err)
		}
		// By forcing a full scan, a new record will be created.
		return m.handleLocalFullScan(ctx, repoPath, repoFullName, headSHA)
	}

	if repoRecord == nil {
		return m.handleLocalFullScan(ctx, repoPath, repoFullName, headSHA)
	}

	return m.handleLocalIncrementalScan(ctx, gitRepo, repoRecord, repoPath, headSHA)
}

var collectionNameRegexp = regexp.MustCompile("[^a-z0-9_-]+")

// GenerateCollectionName builds a valid vector DB collection name from repo and model info.
func GenerateCollectionName(repoFullName, embedderName string) string {
	safeRepoName := strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))
	safeEmbedderName := strings.ToLower(strings.Split(embedderName, ":")[0])

	safeRepoName = collectionNameRegexp.ReplaceAllString(safeRepoName, "")
	safeEmbedderName = collectionNameRegexp.ReplaceAllString(safeEmbedderName, "")

	collectionName := fmt.Sprintf("repo-%s-%s", safeRepoName, safeEmbedderName)

	const maxCollectionNameLength = 255
	if len(collectionName) > maxCollectionNameLength {
		collectionName = collectionName[:maxCollectionNameLength]
	}
	return collectionName
}
