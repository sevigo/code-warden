package repomanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/storage"
)

const cloneTimeout = 5 * time.Minute

// syncRepo decides whether we need a fresh clone or an incremental update.
func (m *manager) syncRepo(ctx context.Context, ev *core.GitHubEvent, token string) (*core.UpdateResult, error) {
	repoRec, err := m.store.GetRepositoryByFullName(ctx, ev.RepoFullName)
	if err != nil {
		return nil, fmt.Errorf("query repository state: %w", err)
	}

	// A model‑change forces a full re‑index.
	if repoRec != nil && repoRec.EmbedderModelName != m.cfg.AI.EmbedderModel {
		m.logger.Warn("embedder model changed, forcing full re‑index",
			"repo", ev.RepoFullName,
			"old", repoRec.EmbedderModelName,
			"new", m.cfg.AI.EmbedderModel,
			"new_collection", GenerateCollectionName(ev.RepoFullName, m.cfg.AI.EmbedderModel),
		)
		if err := m.vectorStore.DeleteCollection(ctx, repoRec.QdrantCollectionName); err != nil {
			m.logger.Warn("delete old qdrant collection failed (might not exist)", "err", err)
		}

		// Update repository record to reflect new model
		repoRec.EmbedderModelName = m.cfg.AI.EmbedderModel
		repoRec.QdrantCollectionName = GenerateCollectionName(ev.RepoFullName, m.cfg.AI.EmbedderModel)
		repoRec.LastIndexedSHA = "" // Reset SHA to force full re-list in incrementalUpdate

		if err := m.store.UpdateRepository(ctx, repoRec); err != nil {
			return nil, fmt.Errorf("failed to update repo record for new embedder: %w", err)
		}
	}

	clonePath := filepath.Join(m.cfg.Storage.RepoPath, ev.RepoFullName)

	if repoRec == nil {
		return m.cloneAndIndex(ctx, ev, token, clonePath)
	}
	return m.incrementalUpdate(ctx, ev, token, repoRec)
}

func (m *manager) cloneAndIndex(
	ctx context.Context,
	ev *core.GitHubEvent,
	token, clonePath string,
) (*core.UpdateResult, error) {
	m.logger.Info("initial clone", "repo", ev.RepoFullName)
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o750); err != nil {
		return nil, fmt.Errorf("create parent dir: %w", err)
	}
	m.cleanupRepoDir(clonePath)

	cloneCtx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()

	_, err := m.gitClient.Clone(cloneCtx, ev.RepoCloneURL, clonePath, token)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, err
	}

	// Fetch PR specific reference to ensure we have the commit (handling forks)
	// Fetch PR specific reference only if it's a PR event
	if ev.PRNumber > 0 {
		prRefSpec := fmt.Sprintf("+refs/pull/%d/head:refs/remotes/origin/pr/%d", ev.PRNumber, ev.PRNumber)
		if err := m.gitClient.Fetch(cloneCtx, clonePath, token, prRefSpec); err != nil {
			m.cleanupRepoDir(clonePath)
			return nil, fmt.Errorf("git fetch pr: %w", err)
		}
	}
	if err = m.gitClient.Checkout(cloneCtx, clonePath, ev.HeadSHA); err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, err
	}

	files, err := m.listRepoFiles(clonePath)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("list files after clone: %w", err)
	}

	// Check if record exists (it might if we are recovering from missing disk files)
	existing, err := m.store.GetRepositoryByFullName(ctx, ev.RepoFullName)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("check existing repo: %w", err)
	}

	newRec := &storage.Repository{
		FullName:             ev.RepoFullName,
		ClonePath:            clonePath,
		QdrantCollectionName: GenerateCollectionName(ev.RepoFullName, m.cfg.AI.EmbedderModel),
		EmbedderModelName:    m.cfg.AI.EmbedderModel,
	}

	if existing != nil {
		// Update existing record
		newRec.ID = existing.ID
		if err = m.store.UpdateRepository(ctx, newRec); err != nil {
			m.cleanupRepoDir(clonePath)
			return nil, fmt.Errorf("update repo record: %w", err)
		}
	} else {
		// Create new record
		if err = m.store.CreateRepository(ctx, newRec); err != nil {
			m.cleanupRepoDir(clonePath)
			return nil, fmt.Errorf("store repo record: %w", err)
		}
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate: files,
		RepoPath:           clonePath,
		HeadSHA:            ev.HeadSHA,
		IsInitialClone:     true,
	}, nil
}

func (m *manager) incrementalUpdate(
	ctx context.Context,
	ev *core.GitHubEvent,
	token string,
	rec *storage.Repository,
) (*core.UpdateResult, error) {
	gitRepo, err := m.gitClient.Open(rec.ClonePath)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			m.logger.Warn("repo missing on disk, falling back to fresh clone", "path", rec.ClonePath)
			return m.cloneAndIndex(ctx, ev, token, rec.ClonePath)
		}
		return nil, err
	}

	// Fetch PR specific reference to ensure we have the commit (handling forks)
	// Fetch PR specific reference only if it's a PR event
	if ev.PRNumber > 0 {
		prRefSpec := fmt.Sprintf("+refs/pull/%d/head:refs/remotes/origin/pr/%d", ev.PRNumber, ev.PRNumber)
		if err = m.gitClient.Fetch(ctx, rec.ClonePath, token, prRefSpec); err != nil {
			return nil, fmt.Errorf("git fetch pr: %w", err)
		}
	} else {
		// Just fetch origin (standard fetch)
		// Fix: Pass token to prevent hanging on private repos (caught by AI review)
		if err = m.gitClient.Fetch(ctx, rec.ClonePath, token); err != nil {
			return nil, fmt.Errorf("git fetch: %w", err)
		}
	}
	if err = m.gitClient.Checkout(ctx, rec.ClonePath, ev.HeadSHA); err != nil {
		return nil, err
	}

	// If no previous SHA recorded, treat as full re-index
	if rec.LastIndexedSHA == "" {
		m.logger.Info("no previous index SHA, listing all files", "repo", ev.RepoFullName)
		files, err := m.listRepoFiles(rec.ClonePath)
		if err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		return &core.UpdateResult{
			FilesToAddOrUpdate: files,
			RepoPath:           rec.ClonePath,
			HeadSHA:            ev.HeadSHA,
			IsInitialClone:     true, // Treat as initial for indexing purposes
		}, nil
	}

	added, modified, deleted, err := m.gitClient.Diff(gitRepo, rec.LastIndexedSHA, ev.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate: append(added, modified...),
		FilesToDelete:      deleted,
		RepoPath:           rec.ClonePath,
		IsInitialClone:     false,
	}, nil
}

func (m *manager) cleanupRepoDir(path string) {
	if err := os.RemoveAll(path); err != nil {
		m.logger.Warn("cleanup failed", "path", path, "err", err)
	}
}
