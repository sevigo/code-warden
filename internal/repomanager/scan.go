package repomanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/storage"
)

func (m *manager) scanLocalRepo(
	ctx context.Context,
	repoPath, repoFullName string,
	force bool,
) (*core.UpdateResult, error) {
	gitRepo, err := m.gitClient.Open(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open local repo: %w", err)
	}

	// Fetch + fast-forward the local branch so HEAD reflects the current remote state.
	// git fetch only updates remote tracking refs; without the merge the local HEAD
	// stays at the old SHA and the incremental scan incorrectly reports "nothing changed".
	// Non-fatal: if either step fails (e.g. offline, no auth, dirty worktree) we
	// continue with the existing local state and log a warning.
	if fetchErr := m.gitClient.Fetch(ctx, repoPath, ""); fetchErr != nil {
		m.logger.Warn("scanLocalRepo: fetch from origin failed, using local state",
			"repo", repoPath, "error", fetchErr)
	} else {
		m.logger.Info("scanLocalRepo: fetched latest from origin", "repo", repoPath)
		if mergeErr := m.gitClient.MergeFF(ctx, repoPath); mergeErr != nil {
			m.logger.Warn("scanLocalRepo: fast-forward merge failed, using local state",
				"repo", repoPath, "error", mergeErr)
		} else {
			m.logger.Info("scanLocalRepo: fast-forwarded local branch to origin", "repo", repoPath)
		}
	}

	// Re-read HEAD SHA after the merge. We cannot use the go-git Repository
	// object opened above because it holds an in-memory cache of refs that is
	// not updated by CLI git commands (fetch + merge). Use the CLI instead so
	// we always read the true post-merge HEAD.
	headSHA, err := m.gitClient.GetHeadSHA(ctx, repoPath)
	if err != nil {
		return nil, fmt.Errorf("read HEAD SHA: %w", err)
	}

	if repoFullName == "" {
		if rec, err := m.store.GetRepositoryByClonePath(ctx, repoPath); err == nil && rec != nil {
			m.logger.Info("found repo record by path", "repo", rec.FullName)
			repoFullName = rec.FullName
		} else {
			if repoFullName, err = m.getRepoFullName(gitRepo); err != nil {
				return nil, fmt.Errorf("auto‑detect repo name: %w", err)
			}
		}
	}

	if force {
		return m.fullLocalScan(ctx, repoPath, repoFullName, headSHA)
	}
	return m.incrementalLocalScan(ctx, gitRepo, repoFullName, repoPath, headSHA)
}

func (m *manager) fullLocalScan(
	ctx context.Context,
	repoPath, repoFullName, headSHA string,
) (*core.UpdateResult, error) {
	// Ensure a repository row exists (create or update the clone path)
	if err := m.ensureRepoRecord(ctx, repoFullName, repoPath); err != nil {
		return nil, err
	}

	files, err := m.listRepoFiles(repoPath)
	if err != nil {
		return nil, fmt.Errorf("list repo files: %w", err)
	}
	return &core.UpdateResult{
		FilesToAddOrUpdate: files,
		RepoPath:           repoPath,
		RepoFullName:       repoFullName,
		HeadSHA:            headSHA,
		IsInitialClone:     true,
	}, nil
}

func (m *manager) incrementalLocalScan(
	ctx context.Context,
	gitRepo *git.Repository,
	repoFullName, repoPath, headSHA string,
) (*core.UpdateResult, error) {
	rec, err := m.store.GetRepositoryByFullName(ctx, repoFullName)
	if err != nil {
		return nil, fmt.Errorf("lookup repo record: %w", err)
	}
	if rec == nil {
		// Should never happen because the caller already checked, but be safe.
		return m.fullLocalScan(ctx, repoPath, repoFullName, headSHA)
	}

	if rec.LastIndexedSHA == headSHA {
		m.logger.Info("nothing changed since last index", "repo", repoFullName)
		return &core.UpdateResult{
			FilesToAddOrUpdate: []string{},
			FilesToDelete:      []string{},
			RepoPath:           repoPath,
			RepoFullName:       repoFullName,
			HeadSHA:            headSHA,
			IsInitialClone:     false,
		}, nil
	}

	added, modified, deleted, err := m.gitClient.Diff(gitRepo, rec.LastIndexedSHA, headSHA)
	if err != nil {
		// As a safety net fall back to a full scan.
		m.logger.Warn("diff failed → full scan", "err", err)
		return m.fullLocalScan(ctx, repoPath, repoFullName, headSHA)
	}

	return &core.UpdateResult{
		FilesToAddOrUpdate: append(added, modified...),
		FilesToDelete:      deleted,
		RepoPath:           repoPath,
		RepoFullName:       repoFullName,
		HeadSHA:            headSHA,
		IsInitialClone:     false,
	}, nil
}

func (m *manager) ensureRepoRecord(ctx context.Context, fullName, clonePath string) error {
	rec, err := m.store.GetRepositoryByFullName(ctx, fullName)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("lookup repo before scan: %w", err)
	}
	if errors.Is(err, storage.ErrNotFound) {
		rec = nil
	}
	if rec == nil {
		newRec := &storage.Repository{
			FullName:             fullName,
			ClonePath:            clonePath,
			QdrantCollectionName: GenerateCollectionName(fullName),
			LastIndexedSHA:       "",
		}
		if err := m.store.CreateRepository(ctx, newRec); err != nil {
			return fmt.Errorf("create repo record: %w", err)
		}
		return nil
	}
	// Update the path – useful when the user moved the repo locally.
	if rec.ClonePath != clonePath {
		rec.ClonePath = clonePath
		if err := m.store.UpdateRepository(ctx, rec); err != nil {
			return fmt.Errorf("update repo path: %w", err)
		}
	}
	return nil
}
