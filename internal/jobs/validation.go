package jobs

import (
	"log/slog"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// validateSuggestions filters out suggestions that reference files not present in the PR
// or have invalid paths.
func validateSuggestions(logger *slog.Logger, suggestions []core.Suggestion, validFiles map[string]struct{}) []core.Suggestion {
	if len(validFiles) == 0 {
		logger.Warn("No valid files provided for validation, skipping validation (risky)")
		return suggestions
	}

	var valid []core.Suggestion
	for _, s := range suggestions {
		// Normalization: trim incompatible prefixes if any (e.g. "./")
		cleanPath := strings.TrimPrefix(s.FilePath, "./")

		if _, exists := validFiles[cleanPath]; exists {
			valid = append(valid, s)
		} else {
			logger.Warn("Filter out suggestion for invalid file", "file", s.FilePath, "suggestion", s)
		}
	}
	return valid
}
