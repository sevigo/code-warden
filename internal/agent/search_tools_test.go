package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/mcp/tools"
)

// newSearchCtx builds a context with the given temp dir as workspace root.
func newSearchCtx(dir string) context.Context {
	return tools.WithProjectRoot(context.Background(), dir)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeSearchFixture(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o750))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
}

// ── grep tests ────────────────────────────────────────────────────────────────

func TestGrepTool_FindsPattern(t *testing.T) {
	dir := t.TempDir()
	writeSearchFixture(t, dir, "src/foo.go", "package foo\n\nfunc Hello() string { return \"hello\" }\n")
	writeSearchFixture(t, dir, "src/bar.go", "package bar\n\nfunc World() {}\n")

	gt := newGrepTool()
	ctx := newSearchCtx(dir)

	result, err := gt.Execute(ctx, map[string]any{"pattern": "Hello"})
	require.NoError(t, err)

	m, ok := result.(map[string]any)
	require.True(t, ok)
	assert.Contains(t, m["output"], "Hello")
	assert.Greater(t, m["count"], 0)
}

func TestGrepTool_GlobFilter(t *testing.T) {
	dir := t.TempDir()
	writeSearchFixture(t, dir, "main.go", "target")
	writeSearchFixture(t, dir, "main.txt", "target")

	gt := newGrepTool()
	ctx := newSearchCtx(dir)

	result, err := gt.Execute(ctx, map[string]any{
		"pattern": "target",
		"glob":    "*.go",
	})
	require.NoError(t, err)

	m := result.(map[string]any)
	output, _ := m["output"].(string)
	assert.Contains(t, output, "main.go")
	assert.NotContains(t, output, "main.txt")
}

func TestGrepTool_LimitRespected(t *testing.T) {
	dir := t.TempDir()
	// Write a file with 10 matching lines.
	content := ""
	for range 10 {
		content += "match here\n"
	}
	writeSearchFixture(t, dir, "file.go", content)

	gt := newGrepTool()
	ctx := newSearchCtx(dir)

	result, err := gt.Execute(ctx, map[string]any{
		"pattern": "match",
		"limit":   3,
	})
	require.NoError(t, err)

	m := result.(map[string]any)
	assert.Equal(t, true, m["limit_reached"])
	// count should be <= 3 + possible blank trailing line
	count, _ := m["count"].(int)
	assert.LessOrEqual(t, count, 4)
}

func TestGrepTool_PathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	gt := newGrepTool()
	ctx := newSearchCtx(dir)

	_, err := gt.Execute(ctx, map[string]any{
		"pattern": "x",
		"path":    "../etc",
	})
	assert.Error(t, err)
}

func TestGrepTool_NoMatchesReturnsEmptyOutput(t *testing.T) {
	dir := t.TempDir()
	writeSearchFixture(t, dir, "a.go", "package a\n")

	gt := newGrepTool()
	ctx := newSearchCtx(dir)

	result, err := gt.Execute(ctx, map[string]any{"pattern": "zzz_nomatch_zzz"})
	require.NoError(t, err)

	m := result.(map[string]any)
	assert.Equal(t, 0, m["count"])
}

// ── find tests ────────────────────────────────────────────────────────────────

func TestFindTool_GlobBasename(t *testing.T) {
	dir := t.TempDir()
	writeSearchFixture(t, dir, "src/foo.go", "")
	writeSearchFixture(t, dir, "src/bar.go", "")
	writeSearchFixture(t, dir, "src/readme.txt", "")

	ft := &findTool{}
	ctx := newSearchCtx(dir)

	result, err := ft.Execute(ctx, map[string]any{"pattern": "*.go"})
	require.NoError(t, err)

	m := result.(map[string]any)
	files, _ := m["files"].([]string)
	assert.Len(t, files, 2)
	for _, f := range files {
		assert.Regexp(t, `\.go$`, f)
	}
}

func TestFindTool_DoubleStarGlob(t *testing.T) {
	dir := t.TempDir()
	writeSearchFixture(t, dir, "a/foo_test.go", "")
	writeSearchFixture(t, dir, "a/b/bar_test.go", "")
	writeSearchFixture(t, dir, "a/nottest.go", "")

	ft := &findTool{}
	ctx := newSearchCtx(dir)

	result, err := ft.Execute(ctx, map[string]any{"pattern": "**/*_test.go"})
	require.NoError(t, err)

	m := result.(map[string]any)
	files, _ := m["files"].([]string)
	assert.Len(t, files, 2)
	for _, f := range files {
		assert.Regexp(t, `_test\.go$`, f)
	}
}

func TestFindTool_SkipsGitDir(t *testing.T) {
	dir := t.TempDir()
	writeSearchFixture(t, dir, ".git/HEAD", "ref: refs/heads/main")
	writeSearchFixture(t, dir, "src/main.go", "")

	ft := &findTool{}
	ctx := newSearchCtx(dir)

	result, err := ft.Execute(ctx, map[string]any{"pattern": "*"})
	require.NoError(t, err)

	m := result.(map[string]any)
	files, _ := m["files"].([]string)
	for _, f := range files {
		assert.NotContains(t, f, ".git")
	}
}

func TestFindTool_LimitRespected(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		writeSearchFixture(t, dir, filepath.Join("src", filepath.FromSlash("file"+string(rune('a'+i))+".go")), "")
	}

	ft := &findTool{}
	ctx := newSearchCtx(dir)

	result, err := ft.Execute(ctx, map[string]any{"pattern": "*.go", "limit": 3})
	require.NoError(t, err)

	m := result.(map[string]any)
	files, _ := m["files"].([]string)
	assert.Len(t, files, 3)
	assert.Equal(t, true, m["truncated"])
}

func TestFindTool_PathEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	ft := &findTool{}
	ctx := newSearchCtx(dir)

	_, err := ft.Execute(ctx, map[string]any{
		"pattern": "*.go",
		"path":    "../etc",
	})
	assert.Error(t, err)
}

func TestFindTool_ScopedToSubdir(t *testing.T) {
	dir := t.TempDir()
	writeSearchFixture(t, dir, "internal/agent/foo.go", "")
	writeSearchFixture(t, dir, "cmd/main.go", "")

	ft := &findTool{}
	ctx := newSearchCtx(dir)

	result, err := ft.Execute(ctx, map[string]any{
		"pattern": "*.go",
		"path":    "internal",
	})
	require.NoError(t, err)

	m := result.(map[string]any)
	files, _ := m["files"].([]string)
	require.Len(t, files, 1)
	assert.Contains(t, files[0], "internal")
}

// ── matchGlob unit tests ──────────────────────────────────────────────────────

func TestMatchGlob_Basename(t *testing.T) {
	assert.True(t, matchGlob("*.go", "foo.go"))
	assert.True(t, matchGlob("*.go", "src/foo.go"))
	assert.False(t, matchGlob("*.go", "foo.txt"))
}

func TestMatchGlob_WithPath(t *testing.T) {
	assert.True(t, matchGlob("internal/agent/*.go", "internal/agent/foo.go"))
	assert.False(t, matchGlob("internal/agent/*.go", "internal/other/foo.go"))
}

func TestMatchGlob_DoubleStar(t *testing.T) {
	assert.True(t, matchGlob("**/*_test.go", "foo_test.go"))
	assert.True(t, matchGlob("**/*_test.go", "a/b/foo_test.go"))
	assert.False(t, matchGlob("**/*_test.go", "a/b/foo.go"))
	assert.True(t, matchGlob("**/*.go", "main.go"))
	assert.True(t, matchGlob("**/*.go", "a/b/c/main.go"))
}

func TestMatchGlob_DoubleStarWithPrefix(t *testing.T) {
	assert.True(t, matchGlob("internal/**/*.go", "internal/agent/foo.go"))
	assert.True(t, matchGlob("internal/**/*.go", "internal/a/b/foo.go"))
	assert.False(t, matchGlob("internal/**/*.go", "cmd/main.go"))
}

// ── read_file continuation hint tests ────────────────────────────────────────

func TestReadFileTool_TruncationHint(t *testing.T) {
	dir := t.TempDir()
	// Write a 20-line file.
	content := ""
	for i := range 20 {
		content += "line " + strconv.Itoa(i+1) + "\n"
	}
	writeSearchFixture(t, dir, "big.go", content)

	rt := &readFileTool{}
	ctx := newSearchCtx(dir)

	result, err := rt.Execute(ctx, map[string]any{
		"path":  "big.go",
		"limit": 5,
	})
	require.NoError(t, err)

	m := result.(map[string]any)
	assert.Equal(t, true, m["truncated"])
	assert.Equal(t, 20, m["total_lines"])
	hint, _ := m["hint"].(string)
	assert.Contains(t, hint, "offset=")
	assert.Contains(t, hint, "20 lines")
}

func TestReadFileTool_NoHintWhenNotTruncated(t *testing.T) {
	dir := t.TempDir()
	writeSearchFixture(t, dir, "small.go", "line1\nline2\n")

	rt := &readFileTool{}
	ctx := newSearchCtx(dir)

	result, err := rt.Execute(ctx, map[string]any{"path": "small.go"})
	require.NoError(t, err)

	m := result.(map[string]any)
	_, hasTruncated := m["truncated"]
	assert.False(t, hasTruncated)
	_, hasHint := m["hint"]
	assert.False(t, hasHint)
}
