package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// setupPRHeadFixture builds a local repo where the default branch has file.go
// at v1, and refs/pull/<prNumber>/head points to a commit with file.go at v2
// that is not reachable from the default branch.
func setupPRHeadFixture(t *testing.T, prNumber int) (repoPath, headSHA string) {
	t.Helper()
	requireGit(t)

	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")

	writeFile(t, dir, "file.go", "package example\n\nconst Version = \"v1\"\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "v1")
	mainSHA := runGitOutput(t, dir, "rev-parse", "HEAD")

	writeFile(t, dir, "file.go", "package example\n\nconst Version = \"v2\"\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "v2")
	headSHA = runGitOutput(t, dir, "rev-parse", "HEAD")

	runGit(t, dir, "update-ref", "refs/pull/"+strconv.Itoa(prNumber)+"/head", headSHA)
	// Reset the default branch back to v1 so v2 is only reachable via the PR ref.
	runGit(t, dir, "reset", "--hard", mainSHA)

	return dir, headSHA
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

func TestClonePRHeadTemp_ChecksOutHeadNotDefaultBranch(t *testing.T) {
	baseRepo, headSHA := setupPRHeadFixture(t, 1)

	client := NewClient(nil)
	workspace, cleanup, err := client.ClonePRHeadTemp(context.Background(), baseRepo, 1, headSHA, "")
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	defer cleanup()

	content, err := os.ReadFile(filepath.Join(workspace, "file.go"))
	require.NoError(t, err)
	assert.Contains(t, string(content), `"v2"`)

	gotHead, err := client.GetHeadSHA(context.Background(), workspace)
	require.NoError(t, err)
	assert.Equal(t, headSHA, gotHead)
}

func TestClonePRHeadTemp_CleanupRemovesWorkspace(t *testing.T) {
	baseRepo, headSHA := setupPRHeadFixture(t, 2)

	client := NewClient(nil)
	workspace, cleanup, err := client.ClonePRHeadTemp(context.Background(), baseRepo, 2, headSHA, "")
	require.NoError(t, err)

	cleanup()
	_, statErr := os.Stat(workspace)
	assert.True(t, os.IsNotExist(statErr), "expected workspace to be removed after cleanup")
}

func TestClonePRHeadTemp_MissingRefFailsWithoutFallback(t *testing.T) {
	baseRepo, headSHA := setupPRHeadFixture(t, 3)

	// A missing ref is a permanent failure, but Fetch retries transient errors
	// with backoff; cap the test's wait instead of waiting out every retry.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client := NewClient(nil)
	// PR #999 was never created in the fixture — no refs/pull/999/head exists.
	_, _, err := client.ClonePRHeadTemp(ctx, baseRepo, 999, headSHA, "")
	require.Error(t, err)
}

func TestClonePRHeadTemp_UnknownSHAFailsWithoutFallback(t *testing.T) {
	baseRepo, _ := setupPRHeadFixture(t, 4)

	client := NewClient(nil)
	_, _, err := client.ClonePRHeadTemp(context.Background(), baseRepo, 4, "0000000000000000000000000000000000000000", "")
	require.Error(t, err)
}

func TestClonePRHeadTemp_MasksTokenInErrors(t *testing.T) {
	requireGit(t)

	const token = "super-secret-installation-token"
	client := NewClient(nil)

	// 127.0.0.1 with an unused low port refuses the connection immediately
	// (loopback only, no DNS lookup, no external network access), but git's
	// own error output echoes back the authenticated URL it tried to reach —
	// exactly what must be masked before the error reaches a caller or a log.
	_, _, err := client.ClonePRHeadTemp(context.Background(), "https://127.0.0.1:1/owner/repo.git", 1, "deadbeef", token)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), token)
}

func TestClient_maskToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		token string
		want  string
	}{
		{
			name:  "masks token",
			input: "fatal: unable to access 'https://x-access-token:abc123@github.com/o/r.git/'",
			token: "abc123",
			want:  "fatal: unable to access 'https://x-access-token:[MASKED]@github.com/o/r.git/'",
		},
		{
			name:  "empty token leaves input untouched",
			input: "some output",
			token: "",
			want:  "some output",
		},
		{
			name:  "token not present leaves input untouched",
			input: "some output",
			token: "abc123",
			want:  "some output",
		},
	}
	client := NewClient(nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, client.maskToken(tt.input, tt.token))
		})
	}
}
