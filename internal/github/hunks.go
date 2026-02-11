package github

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderRegex = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ParseValidLinesFromPatch extracts all line numbers that can receive a comment in a GitHub PR.
// These are the lines present in the "new" side of the diff (the + side).
func ParseValidLinesFromPatch(patch string, logger *slog.Logger) map[int]struct{} {
	validLines := make(map[int]struct{})
	lines := strings.Split(patch, "\n")

	currentLine := -1

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			start, err := parseHunkHeader(line)
			if err != nil {
				if logger != nil && !strings.Contains(err.Error(), "no match") {
					logger.Warn("skipped malformed hunk header", "line", line, "error", err)
				}
				currentLine = -1
				continue
			}
			currentLine = start
			continue
		}

		if currentLine == -1 {
			continue
		}

		// In a unified diff:
		// ' ' (space) is an unchanged line
		// '+' is an added line
		// '-' is a removed line (doesn't increment new line counter)
		switch {
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, " "):
			validLines[currentLine] = struct{}{}
			currentLine++
		case strings.HasPrefix(line, "-"):
			// removal line exists in previous version, not the new one we are commenting on
			continue
		case line == "":
			// empty line usually at end of hunk
			continue
		}
	}

	return validLines
}

func parseHunkHeader(line string) (int, error) {
	matches := hunkHeaderRegex.FindStringSubmatch(line)
	if len(matches) < 2 {
		return -1, fmt.Errorf("no match")
	}
	return strconv.Atoi(matches[1])
}
