package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/mocks"
)

// requireGit skips the test if git is not on PATH.
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

// setupJobWorkspaceFixture builds an "upstream" repo with a PR head ref and a
// local clone of it (with an "origin" remote), modeling what repoMgr.SyncRepo
// keeps on disk: an existing default-branch clone, not yet holding the PR ref.
func setupJobWorkspaceFixture(t *testing.T, prNumber int) (clonePath, upstream, headSHA string) {
	t.Helper()
	requireGit(t)

	upstream = t.TempDir()
	runGit(t, upstream, "init")
	runGit(t, upstream, "config", "user.email", "test@example.com")
	runGit(t, upstream, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(upstream, "file.go"), []byte("package example\n"), 0o600))
	runGit(t, upstream, "add", ".")
	runGit(t, upstream, "commit", "-m", "v1")
	mainSHA := runGitOutput(t, upstream, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(upstream, "file.go"), []byte("package example\n\nfunc Added() {}\n"), 0o600))
	runGit(t, upstream, "add", ".")
	runGit(t, upstream, "commit", "-m", "v2")
	headSHA = runGitOutput(t, upstream, "rev-parse", "HEAD")

	runGit(t, upstream, "update-ref", filepath.Join("refs/pull", itoa(prNumber), "head"), headSHA)
	runGit(t, upstream, "reset", "--hard", mainSHA)

	scratch := t.TempDir()
	clonePath = filepath.Join(scratch, "clone")
	runGit(t, scratch, "clone", upstream, clonePath)

	return clonePath, upstream, headSHA
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestBuildHeadWorkspace_ChecksOutExactHeadCommit(t *testing.T) {
	clonePath, _, headSHA := setupJobWorkspaceFixture(t, 21)

	job := &ReviewJob{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	event := &core.GitHubEvent{RepoFullName: "owner/repo", PRNumber: 21, HeadSHA: headSHA}

	workspace, cleanup, err := job.buildHeadWorkspace(context.Background(), clonePath, event, "")
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	defer cleanup()

	content, err := os.ReadFile(filepath.Join(workspace, "file.go"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "func Added")
}

func TestBuildHeadWorkspace_SupersededWhenPRMoved(t *testing.T) {
	clonePath, upstream, headSHA := setupJobWorkspaceFixture(t, 22)

	// The PR moves to a new commit on GitHub (the upstream) before the job's
	// workspace is built.
	require.NoError(t, os.WriteFile(filepath.Join(upstream, "file.go"), []byte("package example\n\nfunc Moved() {}\n"), 0o600))
	runGit(t, upstream, "add", ".")
	runGit(t, upstream, "commit", "-m", "v3")
	newHeadSHA := runGitOutput(t, upstream, "rev-parse", "HEAD")
	runGit(t, upstream, "update-ref", "refs/pull/22/head", newHeadSHA)

	job := &ReviewJob{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	event := &core.GitHubEvent{RepoFullName: "owner/repo", PRNumber: 22, HeadSHA: headSHA}

	_, _, err := job.buildHeadWorkspace(context.Background(), clonePath, event, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, gitutil.ErrPRHeadSuperseded), "expected ErrPRHeadSuperseded, got: %v", err)
}

func TestBuildHeadWorkspace_OtherFailuresAreNotSuperseded(t *testing.T) {
	clonePath, _, headSHA := setupJobWorkspaceFixture(t, 23)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	job := &ReviewJob{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// PR #999 was never created in the fixture — no refs/pull/999/head exists.
	event := &core.GitHubEvent{RepoFullName: "owner/repo", PRNumber: 999, HeadSHA: headSHA}

	_, _, err := job.buildHeadWorkspace(ctx, clonePath, event, "")
	require.Error(t, err)
	assert.False(t, errors.Is(err, gitutil.ErrPRHeadSuperseded))
}

func TestStartJobRun_MarksSupersededStatusDistinctFromFailed(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	job := &ReviewJob{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	event := &core.GitHubEvent{RepoFullName: "owner/repo", PRNumber: 5}

	store.EXPECT().InsertJobRun(gomock.Any(), gomock.Any()).Return(int64(42), nil)
	store.EXPECT().UpdateJobRun(gomock.Any(), int64(42), "superseded", gomock.Any(), gomock.Any()).Return(nil)

	finish := job.startJobRun(context.Background(), "review", event, "webhook:/review")
	// buildHeadWorkspace wraps gitutil.ErrPRHeadSuperseded in ErrJobSuperseded
	// (see setupReviewEnvironment); startJobRun must recognize it through the
	// wrapping via errors.Is, not by exact equality.
	finish(context.Background(), fmt.Errorf("%w: fetched abc, expected def", ErrJobSuperseded))
}

func TestStartJobRun_MarksFailedStatusForOrdinaryErrors(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	job := &ReviewJob{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	event := &core.GitHubEvent{RepoFullName: "owner/repo", PRNumber: 5}

	store.EXPECT().InsertJobRun(gomock.Any(), gomock.Any()).Return(int64(43), nil)
	store.EXPECT().UpdateJobRun(gomock.Any(), int64(43), "failed", gomock.Any(), gomock.Any()).Return(nil)

	finish := job.startJobRun(context.Background(), "review", event, "webhook:/review")
	finish(context.Background(), errors.New("llm unavailable"))
}

func TestStartJobRun_MarksCompletedStatusOnSuccess(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	job := &ReviewJob{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	event := &core.GitHubEvent{RepoFullName: "owner/repo", PRNumber: 5}

	store.EXPECT().InsertJobRun(gomock.Any(), gomock.Any()).Return(int64(44), nil)
	store.EXPECT().UpdateJobRun(gomock.Any(), int64(44), "completed", gomock.Any(), gomock.Any()).Return(nil)

	finish := job.startJobRun(context.Background(), "review", event, "webhook:/review")
	finish(context.Background(), nil)
}
