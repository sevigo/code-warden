package jobs

import (
	"log/slog"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

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
		cleanPath := strings.TrimPrefix(s.FilePath, "./")
		lines, exists := validLineMaps[cleanPath]
		if !exists {
			logger.Warn("Moving suggestion to general findings (file not in PR)",
				"original", s.FilePath,
				"normalized", cleanPath,
			)
			offDiff = append(offDiff, s)
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
