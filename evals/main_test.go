package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateEvalWorkspaceWritesContextFiles(t *testing.T) {
	t.Parallel()

	workspace, cleanup, err := createEvalWorkspace(EvalCase{
		Diff: "diff --git a/new.go b/new.go\nnew file mode 100644\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,2 @@\n+package example\n+\n",
		WorkspaceFiles: map[string]string{
			"internal/context.go": "package internal\n",
		},
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	newFile, err := os.ReadFile(filepath.Join(workspace, "new.go"))
	require.NoError(t, err)
	require.Equal(t, "package example\n\n", string(newFile))

	contextFile, err := os.ReadFile(filepath.Join(workspace, "internal", "context.go"))
	require.NoError(t, err)
	require.Equal(t, "package internal\n", string(contextFile))
}

func TestCreateEvalWorkspaceRejectsPathsOutsideWorkspace(t *testing.T) {
	t.Parallel()

	_, cleanup, err := createEvalWorkspace(EvalCase{
		WorkspaceFiles: map[string]string{"../outside.go": "unsafe"},
	})
	if cleanup != nil {
		cleanup()
	}
	require.ErrorContains(t, err, "invalid workspace file path")
}

func TestExtractNewFilesHandlesMultipleFiles(t *testing.T) {
	t.Parallel()

	files := extractNewFiles("diff --git a/first.go b/first.go\n--- a/first.go\n+++ b/first.go\n@@ -1 +1 @@\n-old\n+new\ndiff --git a/second.go b/second.go\nnew file mode 100644\n--- /dev/null\n+++ b/second.go\n@@ -0,0 +1 @@\n+package second\n")
	require.Equal(t, "new\n", files["first.go"])
	require.Equal(t, "package second\n", files["second.go"])
}
