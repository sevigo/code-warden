package review

import (
	"fmt"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// severityRank orders severities for dedup: when two passes report the same
// issue at the same location, the higher severity wins.
var severityRank = map[string]int{
	"critical": 4,
	"high":     3,
	"medium":   2,
	"low":      1,
}

// rank returns the numeric severity rank of s.Severity (0 for unknown).
func rank(sev string) int {
	return severityRank[strings.ToLower(sev)]
}

// findingKey builds a deterministic dedup key for a suggestion:
// "file:line:identifier", or "file:line" when no identifier is present.
// Two suggestions with the same key are treated as the same finding.
func findingKey(s core.Suggestion) string {
	file := strings.TrimPrefix(s.FilePath, "./")
	if s.LineNumber > 0 {
		return fmt.Sprintf("%s:%d", file, s.LineNumber)
	}
	return file
}

// Deduplicate merges findings from all passes into a single list. It removes
// exact duplicates (same file:line) and keeps, for each location, the finding
// with the higher severity (ties keep the first-seen pass order).
func Deduplicate(reviews []*core.StructuredReview) []core.Suggestion {
	seen := make(map[string]int) // key -> index in out
	out := make([]core.Suggestion, 0, len(reviews))

	for _, r := range reviews {
		if r == nil {
			continue
		}
		for _, s := range r.Suggestions {
			key := findingKey(s)
			if idx, ok := seen[key]; ok {
				if rank(out[idx].Severity) < rank(s.Severity) {
					out[idx] = s
				}
				continue
			}
			seen[key] = len(out)
			out = append(out, s)
		}
	}
	return out
}
