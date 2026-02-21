package llm

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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
