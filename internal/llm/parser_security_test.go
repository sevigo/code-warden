package llm

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseXMLReview_Security(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantSummary string
		wantVerdict string
		ok          bool
	}{
		{
			name:        "simple extraction",
			content:     "<review><summary>bar</summary></review>",
			wantSummary: "bar",
			ok:          true,
		},
		{
			name:    "no tags",
			content: "just some text",
			ok:      false,
		},
		{
			name:        "unclosed tag",
			content:     "<review><summary>bar",
			wantSummary: "bar",
			ok:          true, // Lenient parsing captures until end
		},
		{
			name:        "nested tags (should return outer content)",
			content:     "<review><summary>fine<foo>ok</foo></summary></review>",
			wantSummary: "fine<foo>ok</foo>",
			ok:          true,
		},
		{
			name:        "malformed end tag",
			content:     "<review><summary>bar</ summary></review>",
			wantSummary: "bar",
			ok:          true, // Lenient parsing captures until end/next tag
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMarkdownReview(context.Background(), tt.content, slog.Default())
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
	input := hugePreamble + "<review><summary>OK</summary></review>"

	start := time.Now()
	_, err := ParseMarkdownReview(context.Background(), input, slog.Default())
	duration := time.Since(start)

	assert.NoError(t, err)
	if duration > 100*time.Millisecond {
		t.Errorf("Parsing took too long: %v", duration)
	}
}
