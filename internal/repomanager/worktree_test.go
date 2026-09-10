package repomanager

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/storage"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoErrorf(t, err, "git %s", strings.Join(args, " "))
	return strings.TrimSpace(string(out))
}

func TestPruneWorktrees_RemovesOrphanedMetadataAcrossKnownRepos(t *testing.T) {
	requireGit(t)

	upstream := t.TempDir()
	runGit(t, upstream, "init")
	runGit(t, upstream, "config", "user.email", "test@example.com")
	runGit(t, upstream, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(upstream, "file.txt"), []byte("v1"), 0o600))
	runGit(t, upstream, "add", ".")
	runGit(t, upstream, "commit", "-m", "v1")
	headSHA := runGitOutput(t, upstream, "rev-parse", "HEAD")

	scratch := t.TempDir()
	clonePath := filepath.Join(scratch, "clone")
	runGit(t, scratch, "clone", upstream, clonePath)

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	gitClient := gitutil.NewClient(logger)

	// Add a worktree, then delete its directory directly — simulating a
	// crash that skipped `git worktree remove` and left orphaned metadata.
	orphan := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, gitClient.WorktreeAdd(context.Background(), clonePath, orphan, headSHA))
	require.NoError(t, os.RemoveAll(orphan))

	listBefore := runGitOutput(t, clonePath, "worktree", "list", "--porcelain")
	require.Contains(t, listBefore, orphan)

	store := &mockStore{repos: map[string]*storage.Repository{
		"owner/repo":         {FullName: "owner/repo", ClonePath: clonePath},
		"owner/never-cloned": {FullName: "owner/never-cloned", ClonePath: filepath.Join(scratch, "does-not-exist")},
		"owner/no-path":      {FullName: "owner/no-path", ClonePath: ""},
	}}
	mgr := New(&config.Config{}, store, gitClient, logger)

	err := mgr.PruneWorktrees(context.Background())
	require.NoError(t, err)

	listAfter := runGitOutput(t, clonePath, "worktree", "list", "--porcelain")
	assert.NotContains(t, listAfter, orphan)
}

func TestPruneWorktrees_PropagatesStoreError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	gitClient := gitutil.NewClient(logger)
	mgr := New(&config.Config{}, &erroringStore{}, gitClient, logger)

	err := mgr.PruneWorktrees(context.Background())
	require.Error(t, err)
}

// erroringStore satisfies storage.Store with GetAllRepositories failing;
// every other method is unused by PruneWorktrees.
type erroringStore struct {
	mockStore
}

func (s *erroringStore) GetAllRepositories(_ context.Context) ([]*storage.Repository, error) {
	return nil, errors.New("boom")
}
