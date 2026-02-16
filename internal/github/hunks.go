package github

import (
	"bufio"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderRegex = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ParseValidLinesFromPatch parses a git patch and returns a map of line numbers
// that are valid for inline comments on the "new" side of the diff.
func ParseValidLinesFromPatch(patch string, logger *slog.Logger) (map[int]struct{}, error) {
	validLines := make(map[int]struct{})
	scanner := bufio.NewScanner(strings.NewReader(patch))

	// Increase buffer to handle minified/generated files with long lines (up to 1MB)
	buf := make([]byte, 4096)
	scanner.Buffer(buf, 1024*1024)

	currentLine := -1 // Initialize to -1 to indicate we are not yet in a hunk

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "@@") {
			// Parse hunk header
			startLine, err := parseHunkHeader(line)
			if err != nil {
				if logger != nil {
					logger.Warn("failed to parse hunk header", "header", line, "error", err)
				}
				currentLine = -1 // Reset on error to avoid processing subsequent lines incorrectly
				continue
			}
			currentLine = startLine
			continue
		}

		// Skip if we haven't found a valid hunk header yet
		if currentLine == -1 {
			continue
		}

		// We only care about added lines (+) and context lines ( ) on the new side.
		// Deleted lines (-) and other diff metadata are ignored and do not
		// increment the line counter for the new version of the file.
		if strings.HasPrefix(line, "+") {
			// Added line
			validLines[currentLine] = struct{}{}
			currentLine++
		} else if strings.HasPrefix(line, " ") {
			// Context line
			validLines[currentLine] = struct{}{}
			currentLine++
		}
	}

	if err := scanner.Err(); err != nil {
		if logger != nil {
			logger.Error("patch scanning failed", "error", err)
		}
		return nil, fmt.Errorf("failed to scan patch: %w", err)
	}

	return validLines, nil
}

func parseHunkHeader(header string) (int, error) {
	matches := hunkHeaderRegex.FindStringSubmatch(header)
	if len(matches) < 2 {
		return -1, fmt.Errorf("invalid hunk header format")
	}

	startLine, err := strconv.Atoi(matches[1])
	if err != nil {
		return -1, fmt.Errorf("failed to parse start line: %w", err)
	}

	return startLine, nil
}
