package llm

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestExtractTag_Security(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		tag      string
		expected string
		ok       bool
	}{
		{
			name:     "simple extraction",
			content:  "<foo>bar</foo>",
			tag:      "foo",
			expected: "bar",
			ok:       true,
		},
		{
			name:    "no tags",
			content: "just some text",
			tag:     "foo",
			ok:      false,
		},
		{
			name:    "unclosed tag",
			content: "<foo>bar",
			tag:     "foo",
			ok:      false,
		},
		{
			name:     "nested tags (should return outer content)",
			content:  "<review><summary>fine</summary></review>",
			tag:      "review",
			expected: "<summary>fine</summary>",
			ok:       true,
		},
		{
			name:    "malformed end tag",
			content: "<foo>bar</ wrong>",
			tag:     "foo",
			ok:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractTag(tt.content, tt.tag)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.expected, got)
			}
		})
	}
}

func TestSanitizePath_Security(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "**`path/to/file.go`**",
			expected: "path/to/file.go",
		},
		{
			input:    "  some/path.txt  ",
			expected: "some/path.txt",
		},
		{
			input:    "\"path/with/quotes\"",
			expected: "path/with/quotes",
		},
		{
			input:    "path*with*stars",
			expected: "pathwithstars",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizePath(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestDoS_PreambleResilience(t *testing.T) {
	// Huge preamble should not crash the parser
	hugePreamble := strings.Repeat("A", 1000000)
	input := hugePreamble + "<review><summary>OK</summary></review>"

	start := time.Now()
	_, err := parseMarkdownReview(input)
	duration := time.Since(start)

	assert.NoError(t, err)
	if duration > 100*time.Millisecond {
		t.Errorf("Parsing took too long: %v", duration)
	}
}
