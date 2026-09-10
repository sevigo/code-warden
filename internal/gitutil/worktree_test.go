package gitutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupLocalCloneFixture models what repomanager keeps on disk: an existing
// clone with an "origin" remote, whose default-branch working tree is at v1.
// refs/pull/<prNumber>/head exists on the upstream but is not yet fetched
// into the clone — FetchPRHeadWorktree must fetch it on demand. upstream is
// returned too, since GitHub-side changes (a PR moving to a new commit) only
// take effect there, not on the clone's local refs.
func setupLocalCloneFixture(t *testing.T, prNumber int) (clonePath, upstream, headSHA string) {
	t.Helper()
	upstream, headSHA = setupPRHeadFixture(t, prNumber)

	scratch := t.TempDir()
	clonePath = filepath.Join(scratch, "clone")
	runGit(t, scratch, "clone", upstream, clonePath)

	return clonePath, upstream, headSHA
}

func TestFetchPRHeadWorktree_ChecksOutHeadNotDefaultBranch(t *testing.T) {
	baseRepo, _, headSHA := setupLocalCloneFixture(t, 11)

	client := NewClient(nil)
	worktree, cleanup, err := client.FetchPRHeadWorktree(context.Background(), baseRepo, 11, headSHA, "")
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	defer cleanup()

	content, err := os.ReadFile(filepath.Join(worktree, "file.go"))
	require.NoError(t, err)
	assert.Contains(t, string(content), `"v2"`)

	gotHead, err := client.GetHeadSHA(context.Background(), worktree)
	require.NoError(t, err)
	assert.Equal(t, headSHA, gotHead)

	// The base repo's own working tree must be untouched (still v1) — the
	// worktree is a separate checkout, not a mutation of the shared clone.
	baseContent, err := os.ReadFile(filepath.Join(baseRepo, "file.go"))
	require.NoError(t, err)
	assert.Contains(t, string(baseContent), `"v1"`)
}

func TestFetchPRHeadWorktree_DoesNotFullyReclone(t *testing.T) {
	baseRepo, _, headSHA := setupLocalCloneFixture(t, 12)

	client := NewClient(nil)
	worktree, cleanup, err := client.FetchPRHeadWorktree(context.Background(), baseRepo, 12, headSHA, "")
	require.NoError(t, err)
	defer cleanup()

	// A linked worktree shares the parent repo's object database via a
	// .git file (not a .git directory) pointing back at baseRepo.
	info, err := os.Stat(filepath.Join(worktree, ".git"))
	require.NoError(t, err)
	assert.False(t, info.IsDir(), ".git should be a file (linked worktree), not a full clone")
}

func TestFetchPRHeadWorktree_CleanupRemovesWorktreeAndPreservesBaseRepo(t *testing.T) {
	baseRepo, _, headSHA := setupLocalCloneFixture(t, 13)

	client := NewClient(nil)
	worktree, cleanup, err := client.FetchPRHeadWorktree(context.Background(), baseRepo, 13, headSHA, "")
	require.NoError(t, err)

	cleanup()

	_, statErr := os.Stat(worktree)
	assert.True(t, os.IsNotExist(statErr), "expected worktree directory to be removed after cleanup")

	// The base repo itself must still be intact and usable after the
	// worktree tied to it is removed.
	_, statErr = os.Stat(filepath.Join(baseRepo, "file.go"))
	assert.NoError(t, statErr)
}

func TestFetchPRHeadWorktree_SupersededWhenRefMovedPastExpectedSHA(t *testing.T) {
	baseRepo, upstream, headSHA := setupLocalCloneFixture(t, 14)

	// Simulate the PR moving to a new commit on GitHub — i.e. on the
	// upstream — after the job decided to review headSHA.
	writeFile(t, upstream, "file.go", "package example\n\nconst Version = \"v3\"\n")
	runGit(t, upstream, "add", ".")
	runGit(t, upstream, "commit", "-m", "v3")
	newHeadSHA := runGitOutput(t, upstream, "rev-parse", "HEAD")
	runGit(t, upstream, "update-ref", "refs/pull/14/head", newHeadSHA)

	client := NewClient(nil)
	_, _, err := client.FetchPRHeadWorktree(context.Background(), baseRepo, 14, headSHA, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPRHeadSuperseded), "expected ErrPRHeadSuperseded, got: %v", err)
}

func TestFetchPRHeadWorktree_MissingRefFailsWithoutFallback(t *testing.T) {
	baseRepo, _, headSHA := setupLocalCloneFixture(t, 15)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client := NewClient(nil)
	_, _, err := client.FetchPRHeadWorktree(ctx, baseRepo, 999, headSHA, "")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrPRHeadSuperseded))
}

func TestWorktreePrune_RemovesMetadataForDeletedWorktreeDir(t *testing.T) {
	baseRepo, _, headSHA := setupLocalCloneFixture(t, 16)

	client := NewClient(nil)
	worktree, _, err := client.FetchPRHeadWorktree(context.Background(), baseRepo, 16, headSHA, "")
	require.NoError(t, err)

	// Simulate a crash: the worktree directory is deleted directly, without
	// going through `git worktree remove`, leaving orphaned admin metadata.
	require.NoError(t, os.RemoveAll(worktree))

	listBefore := runGitOutput(t, baseRepo, "worktree", "list", "--porcelain")
	assert.Contains(t, listBefore, worktree)

	require.NoError(t, client.WorktreePrune(context.Background(), baseRepo))

	listAfter := runGitOutput(t, baseRepo, "worktree", "list", "--porcelain")
	assert.NotContains(t, listAfter, worktree)
}
