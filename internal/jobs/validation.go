package jobs

import (
	"log/slog"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// validateSuggestions filters suggestions into two groups:
// 1. Inline: suggestions that match a valid file and a line number within a PR hunk.
// 2. Off-Diff: suggestions that correspond to a valid file but are on a line not modified in the PR.
// Suggestions for files not in the PR at all are dropped.
func validateSuggestions(logger *slog.Logger, suggestions []core.Suggestion, validLineMaps map[string]map[int]struct{}) ([]core.Suggestion, []core.Suggestion) {
	if len(validLineMaps) == 0 {
		logger.Warn("Valid files map is empty, skipping suggestion validation")
		return suggestions, nil
	}

	var inline []core.Suggestion
	var offDiff []core.Suggestion

	for _, s := range suggestions {
		cleanPath := strings.TrimPrefix(s.FilePath, "./")
		lines, exists := validLineMaps[cleanPath]
		if !exists {
			logger.Warn("Dropped suggestion for file not in PR", "file", s.FilePath)
			continue
		}

		if _, lineExists := lines[s.LineNumber]; lineExists {
			inline = append(inline, s)
		} else {
			logger.Warn("Moving suggestion to general findings (off-diff line)", "file", s.FilePath, "line", s.LineNumber)
			offDiff = append(offDiff, s)
		}
	}
	return inline, offDiff
}
