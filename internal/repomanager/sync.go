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
// IMPORTANT: This function only ever syncs the repository's *default branch* (main/master).
// PR head SHAs are never checked out here; they are used only for logging and DB records.
// This prevents the "linear indexing" corruption where parallel PR webhooks thrash Qdrant.
func (m *manager) syncRepo(ctx context.Context, ev *core.GitHubEvent, token string) (*core.UpdateResult, error) {
	repoRec, err := m.store.GetRepositoryByFullName(ctx, ev.RepoFullName)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("query repository state: %w", err)
	}
	if errors.Is(err, storage.ErrNotFound) {
		repoRec = nil // Explicitly set to nil for clarity
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
	m.logger.Info("initial clone of default branch", "repo", ev.RepoFullName)
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o750); err != nil {
		return nil, fmt.Errorf("create parent dir: %w", err)
	}
	m.cleanupRepoDir(clonePath)

	cloneCtx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()

	// Clone the default branch only — never the PR head.
	// The PR diff is fetched separately via the GitHub API and passed in-memory to the LLM.
	_, err := m.gitClient.Clone(cloneCtx, ev.RepoCloneURL, clonePath, token)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, err
	}

	// Read what HEAD resolved to (the default branch tip).
	defaultBranchSHA, err := m.gitClient.GetHeadSHA(cloneCtx, clonePath)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("get default branch SHA after clone: %w", err)
	}

	files, err := m.listRepoFiles(clonePath)
	if err != nil {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("list files after clone: %w", err)
	}

	// Check if record exists (it might if we are recovering from missing disk files)
	existing, err := m.store.GetRepositoryByFullName(ctx, ev.RepoFullName)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		m.cleanupRepoDir(clonePath)
		return nil, fmt.Errorf("check existing repo: %w", err)
	}

	newRec := &storage.Repository{
		FullName:             ev.RepoFullName,
		ClonePath:            clonePath,
		QdrantCollectionName: GenerateCollectionName(ev.RepoFullName, m.cfg.AI.EmbedderModel),
		EmbedderModelName:    m.cfg.AI.EmbedderModel,
		// LastIndexedSHA is zeroed here; it is set by the job once Qdrant indexing succeeds.
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
		FilesToAddOrUpdate:   files,
		RepoPath:             clonePath,
		HeadSHA:              ev.HeadSHA,       // PR head — for logging/DB only
		DefaultBranchSHA:     defaultBranchSHA, // Default branch tip — persisted to LastIndexedSHA
		DefaultBranchChanged: true,             // Always need a full index on initial clone
		IsInitialClone:       true,
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

	// Fetch and checkout the DEFAULT BRANCH ONLY — not the PR's HeadSHA.
	// This keeps the on-disk working tree and the Qdrant index in sync with main.
	if err := m.ensureDefaultBranch(ctx, ev, token, rec.ClonePath); err != nil {
		return nil, err
	}

	// Get the current default branch SHA after fetch.
	defaultBranchSHA, err := m.gitClient.GetHeadSHA(ctx, rec.ClonePath)
	if err != nil {
		return nil, fmt.Errorf("get default branch SHA: %w", err)
	}

	// If no previous SHA recorded, treat as full re-index
	if rec.LastIndexedSHA == "" {
		m.logger.Info("no previous index SHA, listing all files for full re-index", "repo", ev.RepoFullName)
		files, err := m.listRepoFiles(rec.ClonePath)
		if err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		return &core.UpdateResult{
			FilesToAddOrUpdate:   files,
			RepoPath:             rec.ClonePath,
			HeadSHA:              ev.HeadSHA,
			DefaultBranchSHA:     defaultBranchSHA,
			DefaultBranchChanged: true, // Force full re-index
			IsInitialClone:       true,
		}, nil
	}

	// Check if the default branch has actually moved since our last index.
	if rec.LastIndexedSHA == defaultBranchSHA {
		m.logger.Info("default branch unchanged, no Qdrant update needed",
			"repo", ev.RepoFullName,
			"sha", defaultBranchSHA,
		)
		return &core.UpdateResult{
			RepoPath:             rec.ClonePath,
			HeadSHA:              ev.HeadSHA,
			DefaultBranchSHA:     defaultBranchSHA,
			DefaultBranchChanged: false, // No vector update needed
		}, nil
	}

	// Default branch moved: compute the incremental diff (LastIndexedSHA → defaultBranchSHA).
	added, modified, deleted, err := m.gitClient.Diff(gitRepo, rec.LastIndexedSHA, defaultBranchSHA)
	if err != nil {
		m.logger.Warn("git diff failed, falling back to full re-index",
			"repo", ev.RepoFullName,
			"last_indexed_sha", rec.LastIndexedSHA,
			"default_branch_sha", defaultBranchSHA,
			"error", err,
		)
		// Cleanup corrupted state before re-cloning
		if err := os.RemoveAll(rec.ClonePath); err != nil {
			m.logger.Error("failed to remove repo directory before reclone", "path", rec.ClonePath, "err", err)
			// Continue even if cleanup fails
		}
		return m.cloneAndIndex(ctx, ev, token, rec.ClonePath)
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate:   append(added, modified...),
		FilesToDelete:        deleted,
		RepoPath:             rec.ClonePath,
		HeadSHA:              ev.HeadSHA,
		DefaultBranchSHA:     defaultBranchSHA,
		DefaultBranchChanged: true,
		IsInitialClone:       false,
	}, nil
}

func (m *manager) cleanupRepoDir(path string) {
	if err := os.RemoveAll(path); err != nil {
		m.logger.Warn("cleanup failed", "path", path, "err", err)
	}
}

// ensureDefaultBranch fetches origin and resets the local branch to match the remote upstream.
// It does NOT check out the PR's HeadSHA — that is intentional.
func (m *manager) ensureDefaultBranch(ctx context.Context, ev *core.GitHubEvent, token, clonePath string) error {
	currentSHA, err := m.gitClient.GetHeadSHA(ctx, clonePath)
	needsFullFetch := currentSHA == "" || err != nil

	if needsFullFetch {
		m.logger.Warn("failed to get current HEAD SHA, forcing fetch", "repo", ev.RepoFullName, "err", err)
	}

	fetchErr := m.gitClient.Fetch(ctx, clonePath, token)

	// If we don't have a valid working tree yet, fetch is mandatory.
	if needsFullFetch && fetchErr != nil {
		return fmt.Errorf("git fetch default branch: %w", fetchErr)
	}

	// If fetch failed but we already had a valid working tree, we can limp along with a warning.
	if fetchErr != nil {
		m.logger.Warn("git fetch failed, using existing local state", "repo", ev.RepoFullName, "err", fetchErr)
		return nil
	}

	// Fetch succeeded. Ensure the working tree is advanced to the newly fetched upstream commit.
	resetErr := m.gitClient.ResetToUpstream(ctx, clonePath)
	if resetErr != nil {
		if needsFullFetch {
			return fmt.Errorf("git reset upstream: %w", resetErr)
		}
		m.logger.Warn("git reset upstream failed, index might be slightly stale", "repo", ev.RepoFullName, "err", resetErr)
	}

	return nil
}
