package gitutil

import (
	"fmt"
	"regexp"
	"strings"
)

// Branch name validation constants.
const (
	// MaxBranchNameLength is the maximum length for git branch names (git limit is 255 bytes).
	MaxBranchNameLength = 255
)

// validBranchName matches safe Git branch names: alphanumeric, slashes, hyphens, underscores, dots.
// Rejects empty strings, double dots (..), and requires alphanumeric start/end.
var validBranchName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9/_\-\.]*[a-zA-Z0-9])?$`)

// sanitizeBranchRegex matches invalid git branch name characters.
var sanitizeBranchRegex = regexp.MustCompile(`[\s~^:?*\[\\]`)

// ValidateBranchName checks that a branch name is safe for shell execution and valid for git.
func ValidateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	if len(name) > MaxBranchNameLength {
		return fmt.Errorf("branch name exceeds maximum length of %d bytes", MaxBranchNameLength)
	}
	if !validBranchName.MatchString(name) {
		return fmt.Errorf("branch name %q contains invalid characters", name)
	}
	// Check for consecutive dots which could lead to directory traversal
	if strings.Contains(name, "..") {
		return fmt.Errorf("branch name cannot contain consecutive dots")
	}
	return nil
}

// SanitizeBranch sanitizes a string to be a valid git branch name.
// Git branch names cannot contain spaces, ~, ^, :, *, ?, [, \\, or start with -.
func SanitizeBranch(name string) string {
	sanitized := sanitizeBranchRegex.ReplaceAllString(name, "-")

	// Remove leading/trailing hyphens and dots
	for len(sanitized) > 0 && (sanitized[0] == '-' || sanitized[0] == '.') {
		sanitized = sanitized[1:]
	}
	for len(sanitized) > 0 && (sanitized[len(sanitized)-1] == '-' || sanitized[len(sanitized)-1] == '.') {
		sanitized = sanitized[:len(sanitized)-1]
	}

	// Limit length to 200 characters
	if len(sanitized) > 200 {
		sanitized = sanitized[:200]
	}

	return sanitized
}
