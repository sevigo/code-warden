package reviewcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".diff"):
			_, _ = w.Write([]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"))
		case strings.HasSuffix(r.URL.Path, "/commits"):
			_, _ = w.Write([]byte(`[{"commit":{"message":"fix behavior"}}]`))
		default:
			_, _ = w.Write([]byte(`{"head":{"repo":{"clone_url":"https://github.com/owner/repo.git"}}}`))
		}
	}))
	defer srv.Close()

	oldBase := apiBase
	apiBase = srv.URL
	defer func() { apiBase = oldBase }()

	input, err := NewPRSource(PRInput{Owner: "owner", Repo: "repo", Number: 7, Token: "secret"}).Load(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "owner/repo", input.Repository)
	assert.Contains(t, input.CloneURL, "x-access-token:secret@")
	assert.Equal(t, []string{"fix behavior"}, input.CommitMessages)
	require.Len(t, input.ChangedFiles, 1)
	assert.Equal(t, "a.go", input.ChangedFiles[0].Filename)
}
