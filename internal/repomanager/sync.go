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
		)
		if err := m.vectorStore.DeleteCollection(ctx, repoRec.QdrantCollectionName); err != nil {
			m.logger.Error("delete old qdrant collection", "err", err)
		}
		repoRec = nil // treat as new repository
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

	gitRepo, err := m.gitClient.Clone(cloneCtx, ev.RepoCloneURL, clonePath, token)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, err
	}
	if err = m.gitClient.Checkout(gitRepo, ev.HeadSHA); err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, err
	}

	files, err := m.listRepoFiles(clonePath)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("list files after clone: %w", err)
	}

	newRec := &storage.Repository{
		FullName:             ev.RepoFullName,
		ClonePath:            clonePath,
		QdrantCollectionName: GenerateCollectionName(ev.RepoFullName, m.cfg.AI.EmbedderModel),
		EmbedderModelName:    m.cfg.AI.EmbedderModel,
	}
	if err = m.store.CreateRepository(ctx, newRec); err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("store repo record: %w", err)
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate: files,
		RepoPath:           clonePath,
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

	if err = m.gitClient.Fetch(ctx, gitRepo, token); err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}
	if err = m.gitClient.Checkout(gitRepo, ev.HeadSHA); err != nil {
		return nil, err
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
