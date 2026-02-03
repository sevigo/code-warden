package repomanager

import (
	"context"
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
	headSHA, err := currentHeadSHA(gitRepo)
	if err != nil {
		return nil, err
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

	if rec, _ := m.store.GetRepositoryByFullName(ctx, repoFullName); rec != nil && rec.EmbedderModelName != m.cfg.AI.EmbedderModel {
		m.logger.Warn("embedder model changed – full re‑scan", "repo", repoFullName)
		_ = m.vectorStore.DeleteCollection(ctx, rec.QdrantCollectionName) // ignore error – we still want to continue
		force = true
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
	if err != nil {
		return fmt.Errorf("lookup repo before scan: %w", err)
	}
	if rec == nil {
		// Determine embedder model (could be passed as arg or default)
		embedderModel := m.cfg.AI.EmbedderModel
		newRec := &storage.Repository{
			FullName:             fullName,
			ClonePath:            clonePath,
			QdrantCollectionName: GenerateCollectionName(fullName, embedderModel),
			EmbedderModelName:    embedderModel,
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

func currentHeadSHA(r *git.Repository) (string, error) {
	h, err := r.Head()
	if err != nil {
		return "", fmt.Errorf("git HEAD: %w", err)
	}
	return h.Hash().String(), nil
}
