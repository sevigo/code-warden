package core

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

	// DisableFormatOnWrite disables per-write formatting (goimports/gofmt for Go).
	// This is independent of the batch format_command, which runs before review
	// as long as format_command is set. A project might disable per-write
	// formatting (e.g. their IDE already does it) but still want batch formatting.
	DisableFormatOnWrite bool `yaml:"disable_format_on_write"`

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
