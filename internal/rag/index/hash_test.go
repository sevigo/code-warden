package index

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsLogicFile tests the file type detection for logic files
func TestIsLogicFile(t *testing.T) {
	testCases := []struct {
		filename string
		expected bool
	}{
		{"main.go", true},
		{"internal/rag/rag.go", true},
		{"README.md", false},
		{"docs/api.yaml", false},
		{"Dockerfile", false},
		{".github/workflows/ci.yml", false},
		{"vendor/lib.js", true}, // .js is a code extension
		{"package.json", false},
		{"", false},
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			result := IsLogicFile(tc.filename)
			assert.Equal(t, tc.expected, result)
		})
	}
}
