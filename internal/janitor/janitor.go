package janitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sevigo/code-warden/internal/storage"
)

// Service implements repository lifecycle cleanup.
type Service struct {
	Store       storage.Store
	VectorStore storage.VectorStore
	BasePath    string
	Logger      *slog.Logger
}

// NewService creates a new janitor service.
func NewService(store storage.Store, vectorStore storage.VectorStore, basePath string, logger *slog.Logger) *Service {
	return &Service{
		Store:       store,
		VectorStore: vectorStore,
		BasePath:    basePath,
		Logger:      logger,
	}
}

// CleanOldRepos deletes repositories older than maxAge based on metadata.
// Returns count of cleaned repos and list of their full names.
func (s *Service) CleanOldRepos(ctx context.Context, dryRun bool, maxAge time.Duration) (int, []string, error) {
	// Sanitize base path once
	absPath, err := filepath.Abs(s.BasePath)
absPath, err := filepath.Abs(s.BasePath)
if err != nil {
	return 0, nil, fmt.Errorf("failed to resolve base path: %w", err)
}
cleanBasePath := filepath.Clean(absPath)
		return 0, nil, fmt.Errorf("failed to resolve base path: %w", err)
	}
	s.BasePath = filepath.Clean(absPath)

	repos, err := s.Store.ListReposOlderThan(ctx, time.Now().Add(-maxAge))
	if err != nil {
		return 0, nil, fmt.Errorf("list repos older than %v: %w", maxAge, err)
	}

	cleaned := make([]string, 0, len(repos))
	var mu sync.Mutex
	var eg errgroup.Group

	// Limit concurrency to avoid overwhelming the system
	eg.SetLimit(4)

	for _, repo := range repos {
		eg.Go(func() error {
			if err := s.cleanupRepo(ctx, repo, dryRun); err != nil {
				// We log the error but don't stop the whole process unless context is canceled
				s.Logger.Error("failed to clean repo", "repo", repo.FullName, "err", err)
				return nil // Continue to next repo
			}
			mu.Lock()
			cleaned = append(cleaned, repo.FullName)
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return len(cleaned), cleaned, err
	}

	return len(cleaned), cleaned, nil
}

func (s *Service) cleanupRepo(ctx context.Context, repo *storage.Repository, dryRun bool) error {
	s.Logger.Info("Cleaning up old repository", "repo", repo.FullName, "age_limit_exceeded", true)

	if dryRun {
		s.Logger.Info("dry run: would delete repo", "repo", repo.FullName, "path", repo.ClonePath, "collection", repo.QdrantCollectionName)
		return nil
	}

	var errs []error

	// 1. Delete vector collection (if any)
	if repo.QdrantCollectionName != "" {
		// Use vector store to delete collection
		if err := s.VectorStore.DeleteCollection(ctx, repo.QdrantCollectionName); err != nil {
			// Log but don't stop partial cleanup if possible?
			// Actually, metadata-first implies we should try to clean everything.
			errs = append(errs, fmt.Errorf("delete vector collection %s: %w", repo.QdrantCollectionName, err))
		}
	}

	// 2. Remove clone directory with security validation
	if repo.ClonePath != "" {
		clonePath := filepath.Clean(repo.ClonePath)
		if !strings.HasPrefix(clonePath, s.BasePath+string(filepath.Separator)) && clonePath != s.BasePath {
			errs = append(errs, fmt.Errorf("clone path %s escapes base path %s", clonePath, s.BasePath))
		} else {
			if err := os.RemoveAll(clonePath); err != nil {
				errs = append(errs, fmt.Errorf("remove clone dir %s: %w", clonePath, err))
			}
		}
	}

	// 3. Delete DB record
	// Only delete the DB record if other cleanups were successful or if we want to force cleanup.
	// Typically, if we fail to delete files, we might NOT want to delete the metadata record so we retry later.
	// But if the error is non-recoverable, we might be stuck.
	// For now, if there are errors in FS/Vector, we do NOT delete the DB record, so it gets picked up again.
	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors for %s: %w", repo.FullName, errors.Join(errs...))
	}

	// If vector and FS cleanup succeeded (or weren't applicable), delete the DB record.
	if err := s.Store.DeleteRepository(ctx, repo.ID); err != nil {
		return fmt.Errorf("delete repository record %s: %w", repo.FullName, err)
	}

	return nil
}
