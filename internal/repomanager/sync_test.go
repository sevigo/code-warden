package repomanager

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/storage"
)

// Mock Store
type mockStore struct {
	repos map[string]*storage.Repository
}

func (s *mockStore) GetRepositoryByFullName(_ context.Context, fullName string) (*storage.Repository, error) {
	if r, ok := s.repos[fullName]; ok {
		return r, nil
	}
	return nil, storage.ErrNotFound
}

func (s *mockStore) CreateRepository(_ context.Context, repo *storage.Repository) error {
	if s.repos == nil {
		s.repos = make(map[string]*storage.Repository)
	}
	repo.ID = int64(len(s.repos) + 1)
	s.repos[repo.FullName] = repo
	return nil
}

func (s *mockStore) UpdateRepository(_ context.Context, repo *storage.Repository) error {
	s.repos[repo.FullName] = repo
	return nil
}

// Stubs for other interface methods
func (s *mockStore) SaveReview(_ context.Context, _ *core.Review) error { return nil }
func (s *mockStore) UpdateReviewPublicationStatus(_ context.Context, _ int64, _ string) error {
	return nil
}
func (s *mockStore) GetLatestReviewForPR(_ context.Context, _ string, _ int) (*core.Review, error) {
	return nil, nil
}
func (s *mockStore) GetAllReviewsForPR(_ context.Context, _ string, _ int) ([]*core.Review, error) {
	return nil, nil
}
func (s *mockStore) GetRepositoryByClonePath(_ context.Context, _ string) (*storage.Repository, error) {
	return nil, nil
}
func (s *mockStore) GetRepositoryByID(_ context.Context, _ int64) (*storage.Repository, error) {
	return nil, nil
}
func (s *mockStore) GetAllRepositories(_ context.Context) ([]*storage.Repository, error) {
	return nil, nil
}
func (s *mockStore) GetFilesForRepo(_ context.Context, _ int64) (map[string]storage.FileRecord, error) {
	return nil, nil
}
func (s *mockStore) UpsertFiles(_ context.Context, _ int64, _ []storage.FileRecord) error {
	return nil
}
func (s *mockStore) DeleteFiles(_ context.Context, _ int64, _ []string) error { return nil }
func (s *mockStore) GetScanState(_ context.Context, _ int64) (*storage.ScanState, error) {
	return nil, nil
}
func (s *mockStore) UpsertScanState(_ context.Context, _ *storage.ScanState) error { return nil }
func (s *mockStore) GetReviewsForRepo(_ context.Context, _ string) ([]*core.Review, error) {
	return nil, nil
}
func (s *mockStore) GetReviewStats(_ context.Context) (*storage.ReviewStats, error) {
	return &storage.ReviewStats{}, nil
}
func (s *mockStore) InsertJobRun(_ context.Context, _ *storage.JobRun) (int64, error) { return 0, nil }
func (s *mockStore) UpdateJobRun(_ context.Context, _ int64, _ string, _ time.Time, _ int64) error {
	return nil
}
func (s *mockStore) ListJobRuns(_ context.Context, _, _ int) ([]*storage.JobRun, error) {
	return nil, nil
}

func TestSync_RecoverFromInvalidSHA(t *testing.T) {
	// This test verifies that if LastIndexedSHA is invalid (e.g. force push or GC),
	// the sync process catches the diff error and falls back to a full re-index (new clone).
	// 1. Setup
	tmpDir := t.TempDir()

	remotePath := filepath.Join(tmpDir, "remote")
	localPath := filepath.Join(tmpDir, "local_storage", "test-user", "test-repo")

	// Init Remote
	r, err := git.PlainInit(remotePath, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	// Commit 1
	os.WriteFile(filepath.Join(remotePath, "file1.txt"), []byte("content1"), 0644)
	w.Add("file1.txt")
	commit1, err := w.Commit("commit 1", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = commit1

	// Commit 2
	os.WriteFile(filepath.Join(remotePath, "file2.txt"), []byte("content2"), 0644)
	w.Add("file2.txt")
	commit2, err := w.Commit("commit 2", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Clone SHALLOWLY (Depth: 1) to ensure commit1 is unreachable, forcing a diff error.
	_, err = git.PlainClone(localPath, false, &git.CloneOptions{
		URL:   remotePath,
		Depth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Setup Manager
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RepoPath: filepath.Join(tmpDir, "local_storage"),
		},
	}
	store := &mockStore{
		repos: map[string]*storage.Repository{
			"test-user/test-repo": {
				FullName:       "test-user/test-repo",
				ClonePath:      localPath,
				LastIndexedSHA: commit1.String(), // Missing from shallow clone triggers fallback
			},
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	gitClient := gitutil.NewClient(logger)
	mgr := New(cfg, store, gitClient, logger)

	// Sync should fail diff and fallback because commit1 is absent in shallow clone
	event := &core.GitHubEvent{
		RepoFullName: "test-user/test-repo",
		RepoCloneURL: remotePath, // Raw local path, no file://
		HeadSHA:      commit2.String(),
	}

	res, err := mgr.SyncRepo(context.Background(), event, "")

	// 3. Assertions
	if err != nil {
		t.Fatalf("Expected success due to fallback, but got error: %v", err)
	}

	if !res.IsInitialClone {
		t.Error("Expected IsInitialClone to be true (indicating fallback to full clone)")
	}

	// LastIndexedSHA must be reset to avoid infinite re-index loops.
	// Actual update to headSHA happens in the worker/caller after indexing.
	repo, err := store.GetRepositoryByFullName(context.Background(), "test-user/test-repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo.LastIndexedSHA != "" {
		t.Errorf("Expected LastIndexedSHA to be reset to empty string, got %s", repo.LastIndexedSHA)
	}
}
