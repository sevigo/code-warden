package core

import "strings"

// DefaultExcludedDirs are directories excluded from scanning and indexing by default.
var DefaultExcludedDirs = []string{".git", ".github", "vendor", "node_modules", "target", "build"}

// ValidSourceExtensions contains file extensions eligible for indexing.
var ValidSourceExtensions = map[string]bool{
	".go":   true,
	".js":   true,
	".ts":   true,
	".py":   true,
	".java": true,
	".c":    true,
	".cpp":  true,
	".h":    true,
	".rs":   true,
	".md":   true,
	".json": true,
	".yaml": true,
	".yml":  true,
}

// IsValidExtension reports whether ext (with or without leading dot) is a supported source extension.
func IsValidExtension(ext string) bool {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	return ValidSourceExtensions["."+ext]
}

// BuildExcludeDirs combines default and user-configured directory exclusions.
func BuildExcludeDirs(userExcludes []string) []string {
	seen := make(map[string]struct{}, len(DefaultExcludedDirs)+len(userExcludes))
	result := make([]string, 0, len(DefaultExcludedDirs)+len(userExcludes))

	for _, dir := range DefaultExcludedDirs {
		if _, ok := seen[dir]; !ok {
			seen[dir] = struct{}{}
			result = append(result, dir)
		}
	}
	for _, dir := range userExcludes {
		if _, ok := seen[dir]; !ok {
			seen[dir] = struct{}{}
			result = append(result, dir)
		}
	}
	return result
}

// DefaultExcludedDirsSet returns a map lookup set of default excluded directories.
func DefaultExcludedDirsSet() map[string]bool {
	return map[string]bool{
		".git":         true,
		".github":      true,
		"vendor":       true,
		"node_modules": true,
		"target":       true,
		"build":        true,
	}
}

// RepoConfig represents the structure of the .code-warden.yml file.
type RepoConfig struct {
	// Custom instructions for the LLM prompt.
	CustomInstructions []string `yaml:"custom_instructions"`

	// High-performance exclusion of entire directories by name.
	// Example: ["dist", "build", "docs"]
	ExcludeDirs []string `yaml:"exclude_dirs"`

	// Exclusion of files based on their extension.
	// The leading dot is optional. Example: [".md", "lock", ".log"]
	ExcludeExts []string `yaml:"exclude_exts"`

	// Exclusion of specific files by their relative path.
	// Example: ["config/secrets.json", "scripts/temp.py"]
	ExcludeFiles []string `yaml:"exclude_files"`
}

// DefaultRepoConfig returns a config with default values.
func DefaultRepoConfig() *RepoConfig {
	return &RepoConfig{
		CustomInstructions: []string{},
		ExcludeDirs:        []string{},
		ExcludeExts:        []string{},
		ExcludeFiles:       []string{},
	}
}
