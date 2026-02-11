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
			name: "multiple fences (should take first)",
			input: "```markdown\n" +
				"code\n" +
				"```\n" +
				"garbage\n" +
				"```",
			expected: "code",
		},
		{
			name: "no closing fence",
			input: "```markdown\n" +
				"header\n" +
				"body",
			expected: "header\nbody",
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

func TestParseSuggestionHeader_MaxLineLength_And_ReDoS(t *testing.T) {
	// Test ReDoS resilience (time)
	payload := "## Suggestion [" + strings.Repeat("a:", 1000) + "123]"
	start := time.Now()
	_, _, _ = parseSuggestionHeader(payload)
	duration := time.Since(start)

	if duration > 10*time.Millisecond {
		t.Errorf("Parsing took too long: %v", duration)
	}

	// Test MaxLineLength enforcement (DoS via allocation)
	hugePayload := "## Suggestion [" + strings.Repeat("a", 5000) + ":123]"
	_, _, ok := parseSuggestionHeader(hugePayload)
	if ok {
		t.Errorf("Expected failure for huge payload > maxLineLength, got success")
	}
}

func TestParseSuggestionHeader_FlexibleWhitespace(t *testing.T) {
	tests := []struct {
		input    string
		matches  bool
		filename string
		line     int
	}{
		{
			input:    "##  Suggestion  [internal/main.go:123]", // Double spaces
			matches:  true,
			filename: "internal/main.go",
			line:     123,
		},
		{
			input:    "## SUGGESTION [C:\\path\\to\\file.go:123]",
			matches:  true,
			filename: "C:\\path\\to\\file.go",
			line:     123,
		},
		{
			input:    "## Suggestion [ src/foo.bar : 456 ]", // Spaces inside brackets
			matches:  true,
			filename: "src/foo.bar",
			line:     456,
		},
		{
			input:    "## Suggestion [src/foo.bar: 456]", // Space after colon might be tricky if not handled
			matches:  true,                               // Current implementation expects :123 without space? Let's check.
			filename: "src/foo.bar",
			line:     456,
		},
		{
			input:   "## Suggestion [invalid]",
			matches: false,
		},
		{
			input:   "## Suggestion [:123]", // Empty path
			matches: false,
		},
		{
			input:   "## Suggestion [file.go:-5]", // Negative line
			matches: false,
		},
		{
			input:   "## Suggestion [file.go:0]", // Zero line
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
