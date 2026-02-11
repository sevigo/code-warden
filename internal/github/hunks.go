package github

import (
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderRegex = regexp.MustCompile(`^@@ -\d+,\d+ \+(\d+),(\d+) @@`)

// ParseValidLinesFromPatch extracts all line numbers that can receive a comment in a GitHub PR.
// These are the lines present in the "new" side of the diff (the + side).
func ParseValidLinesFromPatch(patch string) map[int]struct{} {
	validLines := make(map[int]struct{})
	lines := strings.Split(patch, "\n")

	currentLine := -1

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			matches := hunkHeaderRegex.FindStringSubmatch(line)
			if len(matches) == 3 {
				start, _ := strconv.Atoi(matches[1])
				currentLine = start
			}
			continue
		}

		if currentLine == -1 {
			continue
		}

		// In a unified diff:
		// ' ' (space) is an unchanged line
		// '+' is an added line
		// '-' is a removed line
		// Removing (-) does not increment the "new" line counter.
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, " ") {
			validLines[currentLine] = struct{}{}
			currentLine++
		} else if strings.HasPrefix(line, "-") {
			// removal line exists in previous version, not the new one we are commenting on
			continue
		} else if line == "" {
			// empty line usually at end of hunk
			continue
		}
	}

	return validLines
}
