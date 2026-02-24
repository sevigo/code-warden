package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidExtension(t *testing.T) {
	tests := []struct {
		name     string
		ext      string
		expected bool
	}{
		// Valid extensions with leading dot
		{"go extension", ".go", true},
		{"js extension", ".js", true},
		{"ts extension", ".ts", true},
		{"py extension", ".py", true},
		{"java extension", ".java", true},
		{"c extension", ".c", true},
		{"cpp extension", ".cpp", true},
		{"h extension", ".h", true},
		{"rs extension", ".rs", true},
		{"md extension", ".md", true},
		{"json extension", ".json", true},
		{"yaml extension", ".yaml", true},
		{"yml extension", ".yml", true},

		// Valid extensions without leading dot
		{"go without dot", "go", true},
		{"js without dot", "js", true},

		// Invalid extensions
		{"sum extension", ".sum", false},
		{"exe extension", ".exe", false},
		{"dll extension", ".dll", false},
		{"unknown extension", ".xyz", false},
		{"empty string", "", false},

		// Case insensitivity
		{"uppercase GO", ".GO", true},
		{"mixed case Js", ".Js", true},
		{"uppercase without dot", "GO", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidExtension(tt.ext)
			assert.Equal(t, tt.expected, got, "IsValidExtension(%q)", tt.ext)
		})
	}
}

func TestBuildExcludeDirs(t *testing.T) {
	tests := []struct {
		name         string
		userExcludes []string
		expectedLen  int
		contains     []string
		notContains  []string
	}{
		{
			name:         "no user excludes",
			userExcludes: []string{},
			expectedLen:  len(DefaultExcludedDirs),
			contains:     []string{".git", "vendor", "node_modules"},
			notContains:  []string{},
		},
		{
			name:         "user excludes add new dirs",
			userExcludes: []string{"dist", "build"},
			contains:     []string{".git", "vendor", "node_modules", "dist", "build"},
			notContains:  []string{},
		},
		{
			name:         "user excludes duplicate default",
			userExcludes: []string{".git", "vendor", "custom"},
			contains:     []string{".git", "vendor", "custom"},
			notContains:  []string{},
		},
		{
			name:         "empty string in user excludes",
			userExcludes: []string{""},
			contains:     []string{".git", "vendor"},
			notContains:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildExcludeDirs(tt.userExcludes)

			// Verify all expected directories are present
			gotSet := make(map[string]bool)
			for _, dir := range got {
				gotSet[dir] = true
			}

			for _, dir := range tt.contains {
				assert.True(t, gotSet[dir], "expected %q to be in result", dir)
			}

			for _, dir := range tt.notContains {
				assert.False(t, gotSet[dir], "expected %q to NOT be in result", dir)
			}

			// Verify no duplicates
			seen := make(map[string]bool)
			for _, dir := range got {
				assert.False(t, seen[dir], "duplicate directory %q found", dir)
				seen[dir] = true
			}
		})
	}
}

func TestDefaultExcludedDirsSet(t *testing.T) {
	got := DefaultExcludedDirsSet()

	// Verify all default dirs are present
	for _, dir := range DefaultExcludedDirs {
		assert.True(t, got[dir], "expected %q to be in set", dir)
	}

	// Verify the set is derived from DefaultExcludedDirs
	assert.Len(t, got, len(DefaultExcludedDirs), "set size should match slice length")
}

func TestDefaultRepoConfig(t *testing.T) {
	cfg := DefaultRepoConfig()

	assert.NotNil(t, cfg)
	assert.Empty(t, cfg.CustomInstructions)
	assert.Empty(t, cfg.ExcludeDirs)
	assert.Empty(t, cfg.ExcludeExts)
	assert.Empty(t, cfg.ExcludeFiles)
}
