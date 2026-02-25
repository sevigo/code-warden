package llm

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLegacyReview_Security(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantSummary string
		wantVerdict string
		ok          bool
	}{
		{
			name:        "legacy summary",
			content:     "# SUMMARY\nbar",
			wantSummary: "bar",
			ok:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLegacyMarkdownReview(tt.content)
			if !tt.ok {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantSummary, got.Summary)
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
	input := "# SUMMARY\n" + hugePreamble

	start := time.Now()
	_, err := ParseLegacyMarkdownReview(input)
	duration := time.Since(start)

	assert.NoError(t, err)
	if duration > 100*time.Millisecond {
		t.Errorf("Parsing took too long: %v", duration)
	}
}
