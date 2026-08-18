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
