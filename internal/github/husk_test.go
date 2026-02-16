package github

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseValidLinesFromPatch(t *testing.T) {
	// Create a silent logger for tests
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	tests := []struct {
		name     string
		patch    string
		expected []int // Expected keys in the map
	}{
		{
			name: "Simple addition with context",
			patch: `@@ -1,3 +1,4 @@
  context_line_1
+ added_line_2
- removed_line
  context_line_3`,
			// New side lines:
			// 1 (context), 2 (added), 3 (context)
			// removed_line is skipped and doesn't increment the counter
			expected: []int{1, 2, 3},
		},
		{
			name: "Multiple hunks",
			patch: `@@ -1,2 +1,2 @@
  line_1
+ line_2
@@ -10,1 +10,3 @@
  line_10
+ line_11
+ line_12`,
			// Hunk 1: 1, 2
			// Hunk 2: 10, 11, 12
			expected: []int{1, 2, 10, 11, 12},
		},
		{
			name: "Only deletions",
			patch: `@@ -1,3 +1,0 @@
- line_1
- line_2
- line_3`,
			// No lines in the new side exist to be commented on
			expected: []int{},
		},
		{
			name: "Malformed hunk header",
			patch: `@@ invalid header @@
+ added_line_1`,
			// Should reset currentLine to -1 and skip the added line
			expected: []int{},
		},
		{
			name: "Hunk with no comma in range",
			patch: `@@ -1 +1 @@
+ single_line_change`,
			// Regex handles \d+ without (,\d+)?
			expected: []int{1},
		},
		{
			name:     "Empty patch",
			patch:    "",
			expected: []int{},
		},
		{
			name: "Patch with leading noise",
			patch: `diff --git a/file.go b/file.go
index 123..456 100644
--- a/file.go
+++ b/file.go
@@ -1,1 +1,1 @@
+ actual_change`,
			expected: []int{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseValidLinesFromPatch(tt.patch, logger)

			assert.Equal(t, len(tt.expected), len(got), "Map size mismatch")
			for _, line := range tt.expected {
				_, exists := got[line]
				assert.True(t, exists, "Expected line %d to be valid", line)
			}
		})
	}
}

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		header string
		want   int
		err    bool
	}{
		{"@@ -1,3 +1,4 @@", 1, false},
		{"@@ -10,5 +20,5 @@", 20, false},
		{"@@ -1 +5 @@", 5, false},
		{"@@ malformed @@", -1, true},
		{"no header at all", -1, true},
	}

	for _, tt := range tests {
		got, err := parseHunkHeader(tt.header)
		if tt.err {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		}
	}
}
