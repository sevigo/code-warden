package review

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

func TestDiffFilterAllow(t *testing.T) {
	changedFiles := []internalgithub.ChangedFile{
		{
			Filename: "internal/app/app.go",
			Patch: "diff --git a/internal/app/app.go b/internal/app/app.go\n" +
				"index 000000..111111\n" +
				"--- a/internal/app/app.go\n" +
				"+++ b/internal/app/app.go\n" +
				"@@ -10,3 +10,4 @@\n" +
				" func Foo() {\n" +
				"+    return\n" +
				" }\n",
		},
	}

	filter := newDiffFilter(changedFiles)

	require.True(t, filter.Allow(core.Suggestion{FilePath: "internal/app/app.go", LineNumber: 11}))
	require.False(t, filter.Allow(core.Suggestion{FilePath: "internal/app/app.go", LineNumber: 999}))
	// Line 10 is a context line within the hunk — valid.
	require.True(t, filter.Allow(core.Suggestion{FilePath: "internal/app/app.go", LineNumber: 10}))
}

func TestDiffFilterAllowNoLine(t *testing.T) {
	f := newDiffFilter([]internalgithub.ChangedFile{})
	// Suggestions without a line number or file path are retained.
	require.True(t, f.Allow(core.Suggestion{Comment: "off-diff note"}))
	require.True(t, f.Allow(core.Suggestion{FilePath: "missing.go", LineNumber: 3}))
}

func TestDiffFilterSnapExact(t *testing.T) {
	changedFiles := []internalgithub.ChangedFile{
		{
			Filename: "main.go",
			Patch: "diff --git a/main.go b/main.go\n" +
				"@@ -10,3 +10,4 @@\n" +
				" line10\n" +
				"+line11\n" +
				" line12\n",
		},
	}
	f := newDiffFilter(changedFiles)
	got := f.Snap(core.Suggestion{FilePath: "main.go", LineNumber: 11, Comment: "bug"})
	require.NotNil(t, got)
	require.Equal(t, 11, got.LineNumber)
}

func TestDiffFilterSnapNearby(t *testing.T) {
	// Hunk covers lines 10, 11, 12. Agent reports line 14 (off by 2).
	changedFiles := []internalgithub.ChangedFile{
		{
			Filename: "main.go",
			Patch: "diff --git a/main.go b/main.go\n" +
				"@@ -10,3 +10,4 @@\n" +
				" line10\n" +
				"+line11\n" +
				" line12\n",
		},
	}
	f := newDiffFilter(changedFiles)
	got := f.Snap(core.Suggestion{FilePath: "main.go", LineNumber: 14, Comment: "off-by-2"})
	require.NotNil(t, got)
	require.Equal(t, 12, got.LineNumber)
}

func TestDiffFilterSnapTooFar(t *testing.T) {
	// Hunk covers 10-12. Agent reports line 50 — too far to snap.
	changedFiles := []internalgithub.ChangedFile{
		{
			Filename: "main.go",
			Patch: "diff --git a/main.go b/main.go\n" +
				"@@ -10,3 +10,4 @@\n" +
				" line10\n" +
				"+line11\n" +
				" line12\n",
		},
	}
	f := newDiffFilter(changedFiles)
	got := f.Snap(core.Suggestion{FilePath: "main.go", LineNumber: 50, Comment: "way off"})
	require.Nil(t, got)
}

func TestDiffFilterSnapFileNotInDiff(t *testing.T) {
	f := newDiffFilter([]internalgithub.ChangedFile{})
	got := f.Snap(core.Suggestion{FilePath: "other.go", LineNumber: 5, Comment: "off-diff"})
	require.NotNil(t, got)
	require.Equal(t, 5, got.LineNumber)
}

func TestDiffFilterSnapNoLine(t *testing.T) {
	f := newDiffFilter([]internalgithub.ChangedFile{})
	got := f.Snap(core.Suggestion{Comment: "general note"})
	require.NotNil(t, got)
}
