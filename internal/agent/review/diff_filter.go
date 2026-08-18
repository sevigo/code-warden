package review

import (
	"strings"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

// diffFilter holds the valid new-side line ranges per changed file, used to
// drop findings that point at unchanged code (mirrors Kodus's snapLinesToDiff).
type diffFilter struct {
	fileLines map[string]map[int]struct{}
}

// newDiffFilter builds a diff filter from the changed files' patches.
func newDiffFilter(changedFiles []internalgithub.ChangedFile) *diffFilter {
	fileLines := make(map[string]map[int]struct{})
	for _, cf := range changedFiles {
		lines, err := internalgithub.ParseValidLinesFromPatch(cf.Patch, nil)
		if err != nil {
			continue
		}
		fileLines[cf.Filename] = lines
	}
	return &diffFilter{fileLines: fileLines}
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

// stripPrefix removes a leading "./" from a file path for map lookups.
func stripPrefix(p string) string {
	return strings.TrimPrefix(p, "./")
}
