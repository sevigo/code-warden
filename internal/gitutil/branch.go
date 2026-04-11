package gitutil

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
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

// SanitizeCommitMsg cleans a commit message for safe use with git commit -m.
// It replaces all whitespace (including Unicode line/paragraph separators) with
// spaces, strips ASCII control characters (keeping tabs), removes leading dashes
// to prevent git flag injection, and returns a fallback message if the result is
// empty or whitespace-only.
func SanitizeCommitMsg(msg, fallback string) string {
	// Replace all whitespace (including Unicode line/paragraph separators U+2028, U+2029)
	// with spaces, and strip ASCII control characters (keep tabs).
	msg = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case r < 32 && r != '\t':
			return -1
		default:
			return r
		}
	}, msg)

	// Strip leading dashes to prevent git flag injection (e.g., "--file=...").
	msg = strings.TrimLeft(msg, " \t-")
	msg = strings.TrimRight(msg, " \t-")

	if strings.TrimSpace(msg) == "" {
		return fallback
	}
	return msg
}
