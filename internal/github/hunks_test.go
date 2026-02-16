package github

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseValidLinesFromPatch(t *testing.T) {
	tests := []struct {
		name     string
		patch    string
		expected map[int]struct{}
	}{
		{
			name: "Basic added lines",
			patch: `@@ -1,3 +1,4 @@
 line1
+line2
 line3
+line4`,
			expected: map[int]struct{}{
				1: {}, 2: {}, 3: {}, 4: {},
			},
		},
		{
			name: "Deleted lines only",
			patch: `@@ -1,3 +1,1 @@
-line1
-line2
 line3`,
			expected: map[int]struct{}{
				1: {},
			},
		},
		{
			name: "Multiple hunks",
			patch: `@@ -10,2 +10,3 @@
 line10
+line11
 line12
@@ -20,1 +23,2 @@
 line20
+line21`,
			expected: map[int]struct{}{
				10: {}, 11: {}, 12: {},
				23: {}, 24: {},
			},
		},
		{
			name: "Malformed hunk header",
			patch: `@@ -bad +header @@
+line1`,
			expected: map[int]struct{}{},
		},
		{
			name: "No hunks",
			patch: `diff --git a/file b/file
index 123..456 100644
--- a/file
+++ b/file`,
			expected: map[int]struct{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseValidLinesFromPatch(tt.patch, nil)
			if err != nil {
				t.Errorf("ParseValidLinesFromPatch() error = %v", err)
				return
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("ParseValidLinesFromPatch() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestParseValidLinesFromPatch_LargeLine(t *testing.T) {
	// Line size 70KB (exceeds default 64KB bufio.Scanner limit)
	largeLine := strings.Repeat("x", 70*1024)
	patch := fmt.Sprintf("@@ -1,1 +1,2 @@\n %s\n+new line", largeLine)

	got, err := ParseValidLinesFromPatch(patch, nil)
	if err != nil {
		t.Fatalf("ParseValidLinesFromPatch should handle >64KB lines, got error: %v", err)
	}

	expectedLine := 2 // The "+new line" is line 2 on the new side
	if _, ok := got[expectedLine]; !ok {
		t.Errorf("Expected line %d to be valid, but it was not found in %v", expectedLine, got)
	}
}

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantStart int
		wantErr   bool
	}{
		{"Single line range", "@@ -1 +1 @@", 1, false},
		{"Multi line range", "@@ -1,3 +1,4 @@", 1, false},
		{"Mixed range", "@@ -10,2 +20,3 @@", 20, false},
		{"Invalid format", "@@ -1 +1", -1, true},
		{"Non-numeric", "@@ -a +b @@", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHunkHeader(tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseHunkHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantStart {
				t.Errorf("parseHunkHeader() = %v, want %v", got, tt.wantStart)
			}
		})
	}
}
