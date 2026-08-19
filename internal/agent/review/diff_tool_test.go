package review

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetDiffToolAllSections(t *testing.T) {
	tool := newGetDiffTool("DIFF CONTENT", "main.go: 10,11,12", "main.go\nother.go\n")
	result, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)

	m, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "DIFF CONTENT", m["diff"])
	require.Equal(t, "main.go: 10,11,12", m["changed_lines"])
	require.Equal(t, "main.go\nother.go\n", m["files"])
}

func TestGetDiffToolSingleSection(t *testing.T) {
	tool := newGetDiffTool("DIFF", "lines", "files")

	result, err := tool.Execute(context.Background(), map[string]any{"section": "diff"})
	require.NoError(t, err)
	require.Equal(t, "DIFF", result.(map[string]any)["diff"])
	require.NotContains(t, result.(map[string]any), "changed_lines")

	result, err = tool.Execute(context.Background(), map[string]any{"section": "changed_lines"})
	require.NoError(t, err)
	require.Equal(t, "lines", result.(map[string]any)["changed_lines"])

	result, err = tool.Execute(context.Background(), map[string]any{"section": "files"})
	require.NoError(t, err)
	require.Equal(t, "files", result.(map[string]any)["files"])
}

func TestBuildChangedLinesSummary(t *testing.T) {
	fileLines := map[string]map[int]struct{}{
		"main.go": {10: {}, 11: {}, 12: {}},
		"foo.go":  {5: {}, 7: {}},
	}
	got := buildChangedLinesSummary(fileLines)
	require.Contains(t, got, "main.go:")
	require.Contains(t, got, "10")
	require.Contains(t, got, "foo.go:")
}

func TestBuildChangedLinesSummaryEmpty(t *testing.T) {
	require.Empty(t, buildChangedLinesSummary(nil))
}

func TestBuildFilenameIndex(t *testing.T) {
	fileLines := map[string]map[int]struct{}{
		"main.go": {10: {}},
		"foo.go":  {5: {}},
	}
	got := buildFilenameIndex(fileLines)
	require.Contains(t, got, "main.go")
	require.Contains(t, got, "foo.go")
}
