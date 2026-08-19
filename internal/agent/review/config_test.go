package review

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

func TestFilterBySeverityMedium(t *testing.T) {
	rc := Config{MinSeverity: "medium"}
	suggestions := []core.Suggestion{
		{Severity: "Critical", Comment: "c"},
		{Severity: "High", Comment: "h"},
		{Severity: "Medium", Comment: "m"},
		{Severity: "Low", Comment: "l"},
	}
	got := rc.FilterBySeverity(suggestions)
	require.Len(t, got, 3) // critical, high, medium kept; low dropped
}

func TestFilterBySeverityHigh(t *testing.T) {
	rc := Config{MinSeverity: "high"}
	suggestions := []core.Suggestion{
		{Severity: "Critical"},
		{Severity: "High"},
		{Severity: "Medium"},
		{Severity: "Low"},
	}
	got := rc.FilterBySeverity(suggestions)
	require.Len(t, got, 2) // critical, high kept
}

func TestFilterBySeverityLowKeepsAll(t *testing.T) {
	rc := Config{MinSeverity: "low"}
	suggestions := []core.Suggestion{
		{Severity: "Low"},
		{Severity: "High"},
	}
	got := rc.FilterBySeverity(suggestions)
	require.Len(t, got, 2)
}

func TestFilterBySeverityEmptyKeepsAll(t *testing.T) {
	rc := Config{MinSeverity: ""}
	suggestions := []core.Suggestion{
		{Severity: "Low"},
	}
	got := rc.FilterBySeverity(suggestions)
	require.Len(t, got, 1)
}

func TestShouldSkipFileLockfiles(t *testing.T) {
	rc := DefaultConfig()
	require.True(t, rc.ShouldSkipFile("yarn.lock"))
	require.True(t, rc.ShouldSkipFile("package-lock.json"))
	require.True(t, rc.ShouldSkipFile("go.sum"))
	require.True(t, rc.ShouldSkipFile("vendor/foo/bar.go"))
	require.True(t, rc.ShouldSkipFile("node_modules/react/index.js"))
	require.False(t, rc.ShouldSkipFile("internal/app/main.go"))
	require.False(t, rc.ShouldSkipFile("src/index.ts"))
}

func TestShouldSkipFileDoublestar(t *testing.T) {
	rc := Config{
		IgnorePaths: []string{"**/*.gen.go", "**/*.generated.go"},
	}
	require.True(t, rc.ShouldSkipFile("foo/bar.gen.go"))
	require.True(t, rc.ShouldSkipFile("deep/nested/path/file.generated.go"))
	require.False(t, rc.ShouldSkipFile("foo/bar.go"))
}

func TestFilterChangedFiles(t *testing.T) {
	rc := DefaultConfig()
	files := []internalgithub.ChangedFile{
		{Filename: "main.go"},
		{Filename: "go.sum"},
		{Filename: "internal/app/app.go"},
		{Filename: "yarn.lock"},
		{Filename: "vendor/lib/utils.go"},
	}
	filtered, ignored := rc.FilterChangedFiles(files)
	require.Len(t, filtered, 2)
	require.Equal(t, 3, ignored)
	require.Equal(t, "main.go", filtered[0].Filename)
	require.Equal(t, "internal/app/app.go", filtered[1].Filename)
}

func TestShouldSkipReviewAllIgnored(t *testing.T) {
	rc := DefaultConfig()
	files := []internalgithub.ChangedFile{
		{Filename: "yarn.lock"},
		{Filename: "go.sum"},
	}
	skip, reason := rc.ShouldSkipReview(files)
	require.True(t, skip)
	require.Contains(t, reason, "ignore")
}

func TestShouldSkipReviewTooManyFiles(t *testing.T) {
	rc := Config{MaxFiles: 2}
	files := []internalgithub.ChangedFile{
		{Filename: "a.go"},
		{Filename: "b.go"},
		{Filename: "c.go"},
	}
	skip, reason := rc.ShouldSkipReview(files)
	require.True(t, skip)
	require.Contains(t, reason, "too many")
}

func TestShouldSkipReviewOK(t *testing.T) {
	rc := DefaultConfig()
	files := []internalgithub.ChangedFile{
		{Filename: "main.go"},
		{Filename: "app.go"},
	}
	skip, _ := rc.ShouldSkipReview(files)
	require.False(t, skip)
}

func TestFilterAnglesDisabled(t *testing.T) {
	rc := Config{
		EnabledCategories: map[string]bool{
			"bug":      true,
			"security": true,
		},
	}
	angles := DefaultAngles
	got := rc.FilterAngles(angles)
	require.Len(t, got, 2)
	names := angleNames(got)
	require.Contains(t, names, "bug")
	require.Contains(t, names, "security")
	require.NotContains(t, names, "performance")
	require.NotContains(t, names, "conventions")
}

func TestFilterAnglesEmptyKeepsAll(t *testing.T) {
	rc := Config{}
	got := rc.FilterAngles(DefaultAngles)
	require.Len(t, got, 4)
}
