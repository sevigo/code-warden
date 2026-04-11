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
// This is derived from DefaultExcludedDirs to prevent drift.
func DefaultExcludedDirsSet() map[string]bool {
	result := make(map[string]bool, len(DefaultExcludedDirs))
	for _, dir := range DefaultExcludedDirs {
		result[dir] = true
	}
	return result
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

	// VerifyCommands are commands to run before code review (e.g., lint, test).
	// Example: ["make lint", "make test"] or ["go vet ./...", "go test ./..."]
	// If empty, defaults to ["make lint", "make test"].
	VerifyCommands []string `yaml:"verify_commands"`

	// CommandTimeoutSeconds is the per-command timeout for run_command in seconds.
	// Defaults to 300 (5 minutes) when zero. Increase for repos with long test suites.
	CommandTimeoutSeconds int `yaml:"command_timeout_seconds"`

	// DisableAutoFormat disables post-write formatting. Set to true for repos
	// that manage formatting through their own CI pipeline or pre-commit hooks.
	DisableAutoFormat bool `yaml:"disable_auto_format"`

	// FormatCommand is a shell command to run once before the review phase to
	// format all modified files (e.g. "npm run format", "ruff format .").
	// If empty, no batch formatting is performed.
	FormatCommand string `yaml:"format_command"`
}

// DefaultRepoConfig returns a config with default values.
func DefaultRepoConfig() *RepoConfig {
	return &RepoConfig{
		CustomInstructions: []string{},
		ExcludeDirs:        []string{},
		ExcludeExts:        []string{},
		ExcludeFiles:       []string{},
		VerifyCommands:     []string{}, // Empty means use agent defaults
	}
}
