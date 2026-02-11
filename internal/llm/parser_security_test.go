package llm

import (
	"strings"
	"testing"
	"time"
)

func TestStripMarkdownFence_Security(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "trailing content after fence",
			input: "```markdown\n" +
				"header\n" +
				"```\n" +
				"some trailing garbage",
			expected: "header",
		},
		{
			name: "no closing fence",
			input: "```markdown\n" +
				"header\n" +
				"body",
			expected: "header\nbody",
		},
		{
			name: "nested fences (should take outer)",
			input: "```markdown\n" +
				"code:\n" +
				"```go\n" +
				"func main() {}\n" +
				"```\n" +
				"```",
			expected: "code:\n```go\nfunc main() {}\n```",
		},
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdownFence(tt.input)
			if got != tt.expected {
				t.Errorf("stripMarkdownFence() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestParseSuggestionHeader_ReDoS(t *testing.T) {
	// Construct a payload that would trigger catastrophic backtracking in greedy regex
	// e.g. "## Suggestion [a:a:a:...:123]"
	// Manual parsing should handle this instantly (linear time).

	payload := "## Suggestion [" + strings.Repeat("a:", 10000) + "123]"

	start := time.Now()
	_, _, ok := parseSuggestionHeader(payload)
	duration := time.Since(start)

	if duration > 10*time.Millisecond {
		t.Errorf("Parsing took too long: %v (potential ReDoS or slow parsing)", duration)
	}

	if ok {
		// It might be valid if "a:a:...:a" is considered a filename.
		// Our parser allows it (it just splits on last colon).
		// That's fine, as long as it's fast.
	}
}

func TestParseSuggestionHeader_WindowsPaths(t *testing.T) {
	// Verify that legitimate Windows paths work.
	// We want to support:
	// "## Suggestion [C:\path\to\file.go:123]"
	// "## Suggestion [internal/main.go:123]"

	tests := []struct {
		input    string
		matches  bool
		filename string
		line     int
	}{
		{
			input:    "## Suggestion [internal/main.go:123]",
			matches:  true,
			filename: "internal/main.go",
			line:     123,
		},
		{
			input:    "## Suggestion [C:\\path\\to\\file.go:123]",
			matches:  true,
			filename: "C:\\path\\to\\file.go",
			line:     123,
		},
		{
			input:   "## Suggestion [src/foo.bar: 456]", // Space after colon might be tricky if not handled
			matches: true,                               // Current implementation expects :123 without space? Let's check.
			// The implementation does `strings.TrimSpace(content[lastColon+1:])` then Atoi.
			// " 456" trims to "456". So it SHOULD match.
			filename: "src/foo.bar",
			line:     456,
		},
		{
			input:   "## Suggestion [invalid]",
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			file, line, ok := parseSuggestionHeader(tt.input)

			if !tt.matches {
				if ok {
					t.Errorf("Expected no match, got %q:%d", file, line)
				}
				return
			}

			if !ok {
				t.Errorf("Expected match, got none")
				return
			}
			if file != tt.filename {
				t.Errorf("Filename: got %q, want %q", file, tt.filename)
			}
			if line != tt.line {
				t.Errorf("Line: got %d, want %d", line, tt.line)
			}
		})
	}
}
