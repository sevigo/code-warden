package jobs

import (
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// nonReviewableExtensions contains file extensions that should not be code-reviewed.
// These are documentation, configuration, data, or binary files.
var nonReviewableExtensions = map[string]bool{
	// Documentation
	".md": true, ".markdown": true, ".rst": true, ".adoc": true,
	// Configuration
	".yml": true, ".yaml": true, ".json": true, ".jsonc": true,
	".toml": true, ".ini": true, ".cfg": true, ".conf": true,
	".env": true, ".editorconfig": true, ".gitignore": true,
	// Lock files
	".lock": true, ".sum": true,
	// Data files
	".txt": true, ".csv": true, ".xml": true,
	// Binary/Assets
	".svg": true, ".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".ico": true, ".webp": true, ".pdf": true,
	".zip": true, ".tar": true, ".gz": true,
	// Templates/Prompts
	".prompt": true, ".tmpl": true, ".mustache": true,
	// Generated/Minified
	".min.js": true, ".min.css": true,
}

// codeExtensions contains file extensions that are definitely code files.
// Files with these extensions will always be reviewed.
var codeExtensions = map[string]bool{
	".go": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
	".py": true, ".java": true, ".c": true, ".cpp": true, ".h": true,
	".hpp": true, ".rs": true, ".rb": true, ".php": true, ".cs": true,
	".swift": true, ".kt": true, ".scala": true, ".lua": true,
	".sh": true, ".bash": true, ".zsh": true, ".ps1": true,
	".sql": true, ".vue": true, ".svelte": true,
}

// FilterNonCodeSuggestions removes suggestions for non-reviewable files.
// Non-reviewable files include documentation, configuration, data, and binary files.
func FilterNonCodeSuggestions(logger *slog.Logger, suggestions []core.Suggestion) []core.Suggestion {
	var filtered []core.Suggestion
	for _, s := range suggestions {
		if isReviewableFile(s.FilePath) {
			filtered = append(filtered, s)
		} else {
			logger.Debug("Filtering out non-code file suggestion",
				"file", s.FilePath,
				"line", s.LineNumber,
				"severity", s.Severity,
			)
		}
	}
	return filtered
}

// isReviewableFile determines if a file should be code-reviewed.
// Returns true for code files and files without recognized extensions.
// Returns false for documentation, config, data, and binary files.
func isReviewableFile(path string) bool {
	// Normalize path
	path = strings.ToLower(path)
	path = strings.TrimPrefix(path, "./")

	// Handle special cases with compound extensions FIRST
	// These take precedence over simple extensions
	if strings.HasSuffix(path, ".min.js") ||
		strings.HasSuffix(path, ".min.css") ||
		strings.HasSuffix(path, ".d.ts") {
		return false
	}

	// Get extension
	ext := filepath.Ext(path)

	// Check if it's a known code extension - always review
	if codeExtensions[ext] {
		return true
	}

	// Handle files without extensions (like Makefile, Dockerfile)
	if ext == "" {
		base := filepath.Base(path)
		// Common build/config files without extensions
		switch base {
		case "makefile", "dockerfile", "rakefile", "gemfile", "procfile":
			return false // These are config/build files
		}
		// Unknown files without extension - review them (could be scripts)
		return true
	}

	// Check if it's explicitly non-reviewable
	if nonReviewableExtensions[ext] {
		return false
	}

	// Unknown extension - err on the side of reviewing
	// This catches edge cases like .proto, .graphql, etc.
	return true
}

// ValidateSuggestionsByLine validates suggestions against patch diff lines.
// Returns two slices: inline (on-diff) and offDiff (non-diff) suggestions.
// Both must be posted separately by callers (e.g., GitHub comments vs. PR body).
func ValidateSuggestionsByLine(logger *slog.Logger, suggestions []core.Suggestion, validLineMaps map[string]map[int]struct{}) ([]core.Suggestion, []core.Suggestion) {
	if len(validLineMaps) == 0 {
		logger.Warn("Valid files map is empty, skipping suggestion validation")
		return suggestions, nil
	}

	var inline []core.Suggestion
	var offDiff []core.Suggestion

	for _, s := range suggestions {
		originalPath := s.FilePath
		cleanPath := strings.TrimPrefix(s.FilePath, "./")
		lines, exists := validLineMaps[cleanPath]
		if !exists {
			logger.Warn("Dropping suggestion (file not in PR)",
				"original", originalPath,
				"normalized", cleanPath,
			)
			continue
		}

		if _, lineExists := lines[s.LineNumber]; lineExists {
			inline = append(inline, s)
		} else {
			logger.Warn("Moving suggestion to general findings (off-diff line)",
				"original", s.FilePath,
				"normalized", cleanPath,
				"line", s.LineNumber,
			)
			offDiff = append(offDiff, s)
		}
	}
	return inline, offDiff
}
