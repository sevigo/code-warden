package review

import (
	"path"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

// Config controls which findings are kept and which files are reviewed.
// Inspired by Kodus's suggestion_control + general config: severity filtering,
// ignored paths, and per-category toggles reduce noise and skip irrelevant
// files before spending tokens on them.
type Config struct {
	// MinSeverity is the lowest severity to keep. "low" keeps everything,
	// "medium" drops low, "high" drops medium+low, "critical" keeps only
	// critical. Empty defaults to "low" (keep all).
	MinSeverity string

	// IgnorePaths are glob patterns matching files to skip entirely (no
	// diff sent to the agent, no findings accepted from them). Common
	// examples: yarn.lock, package-lock.json, vendor/**, **/*.gen.go.
	IgnorePaths []string

	// EnabledCategories toggles which review angles run. When nil or empty,
	// all angles run. Keys are angle names: "bug", "security",
	// "performance", "conventions".
	EnabledCategories map[string]bool

	// MaxFiles caps the number of changed files in a PR before the review
	// is skipped. 0 means no limit. Kodus defaults to 10 (trial) / 100.
	// Large diffs reduce review quality and waste tokens.
	MaxFiles int
}

// DefaultConfig returns sensible defaults matching Kodus's shipped
// defaults: min severity medium, common lockfiles ignored, all categories on.
func DefaultConfig() Config {
	return Config{
		MinSeverity: "medium",
		IgnorePaths: []string{
			"yarn.lock",
			"package-lock.json",
			"pnpm-lock.yaml",
			"go.sum",
			"go.mod",
			"package.json",
			".env",
			"vendor/**",
			"node_modules/**",
			"**/*.gen.go",
			"**/*.generated.go",
			"**/*.min.js",
			"**/*.min.css",
		},
		MaxFiles: 100,
	}
}

// ShouldSkipFile reports whether a changed file matches an ignore pattern.
func (c *Config) ShouldSkipFile(filename string) bool {
	for _, pattern := range c.IgnorePaths {
		if matchIgnorePattern(pattern, filename) {
			return true
		}
	}
	return false
}

// matchIgnorePattern matches a glob pattern against a filename. Supports
// ** for multi-segment wildcards.
func matchIgnorePattern(pattern, filename string) bool {
	pattern = path.Clean(pattern)
	filename = path.Clean(filename)

	if !strings.Contains(pattern, "**") {
		if !strings.Contains(pattern, "/") {
			matched, _ := path.Match(pattern, path.Base(filename))
			return matched
		}
		matched, _ := path.Match(pattern, filename)
		return matched
	}
	return matchIgnoreDoublestar(pattern, filename)
}

// matchIgnoreDoublestar matches a pattern containing ** against a filename.
// ** matches zero or more path segments.
func matchIgnoreDoublestar(pattern, s string) bool {
	prefix, after, _ := strings.Cut(pattern, "**")
	rest := strings.TrimPrefix(after, "/")
	if prefix != "" {
		if !strings.HasPrefix(s+"/", prefix) {
			return false
		}
		s = strings.TrimPrefix(s, strings.TrimSuffix(prefix, "/"))
		s = strings.TrimPrefix(s, "/")
	}
	if rest == "" {
		return true
	}
	parts := strings.Split(s, "/")
	for i := 0; i <= len(parts); i++ {
		candidate := strings.Join(parts[i:], "/")
		if matched, _ := path.Match(rest, candidate); matched {
			return true
		}
	}
	return false
}

// FilterBySeverity drops suggestions below the configured minimum severity.
func (c *Config) FilterBySeverity(suggestions []core.Suggestion) []core.Suggestion {
	minRank := rank(c.MinSeverity)
	if minRank <= 0 {
		return suggestions
	}
	out := make([]core.Suggestion, 0, len(suggestions))
	for _, s := range suggestions {
		if rank(s.Severity) >= minRank {
			out = append(out, s)
		}
	}
	return out
}

// FilterChangedFiles splits changed files into reviewed and ignored sets.
// Files matching IgnorePaths are dropped. Returns the filtered list and the
// count of ignored files (for logging).
func (c *Config) FilterChangedFiles(files []internalgithub.ChangedFile) ([]internalgithub.ChangedFile, int) {
	if len(c.IgnorePaths) == 0 {
		return files, 0
	}
	filtered := make([]internalgithub.ChangedFile, 0, len(files))
	ignored := 0
	for _, f := range files {
		if c.ShouldSkipFile(f.Filename) {
			ignored++
			continue
		}
		filtered = append(filtered, f)
	}
	return filtered, ignored
}

// ShouldSkipReview reports whether the review should be skipped entirely
// based on the changed files (too many files or all ignored).
func (c *Config) ShouldSkipReview(files []internalgithub.ChangedFile) (bool, string) {
	filtered, ignored := c.FilterChangedFiles(files)
	if len(filtered) == 0 && ignored > 0 {
		return true, "all changed files match ignore patterns"
	}
	if c.MaxFiles > 0 && len(filtered) > c.MaxFiles {
		return true, "too many changed files"
	}
	return false, ""
}

// FilterAngles drops angles whose categories are disabled in EnabledCategories.
// When the map is non-empty, only angles explicitly set to true are kept.
// When the map is empty, all angles are kept.
func (c *Config) FilterAngles(angles []Angle) []Angle {
	if len(c.EnabledCategories) == 0 {
		return angles
	}
	out := make([]Angle, 0, len(angles))
	for _, a := range angles {
		if enabled, ok := c.EnabledCategories[a.Name]; ok && enabled {
			out = append(out, a)
		}
	}
	return out
}
