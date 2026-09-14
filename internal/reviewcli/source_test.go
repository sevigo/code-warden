package reviewcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalSourceLoad(t *testing.T) {
	requireGit(t)

	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.go"), []byte("package example\n"), 0o600))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.go"), []byte("package example\n\nfunc Added() {}\n"), 0o600))

	input, err := NewLocalSource(dir, "").Load(context.Background())

	require.NoError(t, err)
	assert.Equal(t, filepath.Base(dir), input.Repository)
	assert.Equal(t, dir, input.WorkspaceDir)
	assert.Empty(t, input.CloneURL)
	assert.Contains(t, input.Diff, "func Added")
	require.Len(t, input.ChangedFiles, 1)
	assert.Equal(t, "file.go", input.ChangedFiles[0].Filename)
	assert.Contains(t, input.CommitMessages, "initial commit")
}

func TestLocalSourceRejectsNonDirectory(t *testing.T) {
	t.Parallel()

	_, err := NewLocalSource(filepath.Join(t.TempDir(), "missing"), "").Load(context.Background())
	require.ErrorContains(t, err, "not a directory")
}

func TestPRSourceLoad(t *testing.T) {
	requireGit(t)

	// Local base repo standing in for the PR's base repository: main has
	// file.go at v1, refs/pull/7/head points at a commit with file.go at v2.
	baseRepo := t.TempDir()
	runGit(t, baseRepo, "init")
	runGit(t, baseRepo, "config", "user.email", "test@example.com")
	runGit(t, baseRepo, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(baseRepo, "file.go"), []byte("package example\n"), 0o600))
	runGit(t, baseRepo, "add", ".")
	runGit(t, baseRepo, "commit", "-m", "v1")
	mainSHA := gitOutput(t, baseRepo, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(baseRepo, "file.go"), []byte("package example\n\nfunc Added() {}\n"), 0o600))
	runGit(t, baseRepo, "add", ".")
	runGit(t, baseRepo, "commit", "-m", "v2")
	headSHA := gitOutput(t, baseRepo, "rev-parse", "HEAD")
	runGit(t, baseRepo, "update-ref", "refs/pull/7/head", headSHA)
	runGit(t, baseRepo, "reset", "--hard", mainSHA)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".diff"):
			_, _ = w.Write([]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"))
		case strings.HasSuffix(r.URL.Path, "/commits"):
			_, _ = w.Write([]byte(`[{"commit":{"message":"fix behavior"}}]`))
		default:
			body := `{"head":{"sha":"` + headSHA + `"},"base":{"repo":{"clone_url":"` + baseRepo + `"}}}`
			_, _ = w.Write([]byte(body))
		}
	}))
	defer srv.Close()

	oldBase := apiBase
	apiBase = srv.URL
	defer func() { apiBase = oldBase }()

	source := NewPRSource(PRInput{Owner: "owner", Repo: "repo", Number: 7})
	input, err := source.Load(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "owner/repo", input.Repository)
	assert.Empty(t, input.CloneURL)
	assert.NotEmpty(t, input.WorkspaceDir)
	assert.Equal(t, []string{"fix behavior"}, input.CommitMessages)
	require.Len(t, input.ChangedFiles, 1)
	assert.Equal(t, "a.go", input.ChangedFiles[0].Filename)

	content, err := os.ReadFile(filepath.Join(input.WorkspaceDir, "file.go"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "func Added")

	workspace := input.WorkspaceDir
	source.Close()
	_, statErr := os.Stat(workspace)
	assert.True(t, os.IsNotExist(statErr), "expected workspace to be removed after Close")
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
