package reviewtools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBuildsReadOnlyWorkspaceTools(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n"), 0o600))

	tools := New(workspace)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	assert.ElementsMatch(t, []string{"grep", "find", "read_file", "list_dir"}, names)

	var readTool interface {
		Execute(context.Context, map[string]any) (any, error)
	}
	for _, tool := range tools {
		if tool.Name() == "read_file" {
			readTool = tool
			break
		}
	}
	require.NotNil(t, readTool)
	result, err := readTool.Execute(context.Background(), map[string]any{"path": "main.go"})
	require.NoError(t, err)
	resultMap, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "package main\n", resultMap["content"])
}

func TestReadFileRejectsWorkspaceEscape(t *testing.T) {
	t.Parallel()

	tool := NewReadFile(t.TempDir())
	_, err := tool.Execute(context.Background(), map[string]any{"path": "../outside.go"})

	require.ErrorContains(t, err, "escapes workspace root")
}
