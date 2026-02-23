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
