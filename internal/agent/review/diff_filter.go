package review

import (
	"sort"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

// diffFilter holds the valid new-side line ranges per changed file, used to
// validate and snap findings to the diff hunks (mirrors Kodus's snapLinesToDiff).
type diffFilter struct {
	fileLines map[string]map[int]struct{}
}

// newDiffFilter builds a diff filter from the changed files' patches.
func newDiffFilter(changedFiles []internalgithub.ChangedFile) *diffFilter {
	return &diffFilter{fileLines: internalgithub.BuildValidLineMap(changedFiles)}
}

// Allow reports whether a suggestion points at a valid new-side line in the
// diff. Suggestions without a line number or file path are retained (they are
// handled as off-diff summary comments downstream). Suggestions pointing at a
// line that exists in the file map but is NOT a changed/context line are dropped.
func (f *diffFilter) Allow(s core.Suggestion) bool {
	if s.FilePath == "" || s.LineNumber == 0 {
		return true
	}
	lines, ok := f.fileLines[stripPrefix(s.FilePath)]
	if !ok {
		// File not present in the diff (or its patch failed to parse).
		// Retain so downstream validation can classify it as off-diff.
		return true
	}
	_, inDiff := lines[s.LineNumber]
	return inDiff
}

// Snap validates a suggestion against the diff and, when the cited line is
// close to a hunk, snaps it to the nearest valid diff line. This fixes the
// common failure mode where the agent is off by a few lines (e.g. it reports
// line 50 but the hunk covers 45-48) and the finding would otherwise be
// silently dropped. Returns nil when the finding is outside the file's diff
// hunks entirely (file not in diff -> retain as-is via a copy).
func (f *diffFilter) Snap(s core.Suggestion) *core.Suggestion {
	if s.FilePath == "" || s.LineNumber == 0 {
		return &s
	}
	lines, ok := f.fileLines[stripPrefix(s.FilePath)]
	if !ok {
		// File not in the diff map — retain as off-diff summary comment.
		return &s
	}
	if _, inDiff := lines[s.LineNumber]; inDiff {
		return &s
	}
	// Snap to the nearest valid line within a small window. Kodus uses a
	// similar approach to avoid losing real findings due to off-by-N errors.
	const snapWindow = 5
	sorted := sortedLines(lines)
	bestLine := 0
	bestDelta := snapWindow + 1
	for _, ln := range sorted {
		delta := ln - s.LineNumber
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			bestDelta = delta
			bestLine = ln
		}
	}
	if bestLine > 0 {
		out := s
		out.LineNumber = bestLine
		return &out
	}
	return nil
}

// sortedLines returns the valid line numbers for a file in ascending order.
func sortedLines(lines map[int]struct{}) []int {
	out := make([]int, 0, len(lines))
	for ln := range lines {
		out = append(out, ln)
	}
	sort.Ints(out)
	return out
}

// stripPrefix removes a leading "./" from a file path for map lookups.
func stripPrefix(p string) string {
	return strings.TrimPrefix(p, "./")
}

// NewDiffFilterForTest builds a diff filter for use in tests/evals.
func NewDiffFilterForTest(changedFiles []internalgithub.ChangedFile) *diffFilter {
	return newDiffFilter(changedFiles)
}

// SnapForTest exposes Snap for tests/evals.
func (f *diffFilter) SnapForTest(s core.Suggestion) *core.Suggestion {
	return f.Snap(s)
}
