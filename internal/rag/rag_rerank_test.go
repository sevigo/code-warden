package rag

import (
	"testing"

	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
)

func TestStripPatchNoise(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"only metadata", "--- a/file.go\n+++ b/file.go\n@@ -1,2 +1,3 @@\n", ""},
		{"mixed", "--- a/file.go\n+func X() {}\n- func Y() {}\n@@ -1,2 +1,3 @@\n", "+func X() {}"},
		{"hyde header + patch", "To understand... \n+func X() {}\n", "To understand... \n+func X() {}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripPatchNoise(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPreFilterBM25(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		docs      []schema.Document
		topK      int
		expectLen int
	}{
		{"topK >= len", "foo", make([]schema.Document, 3), 5, 3},
		{"topK < len", "foo bar", make([]schema.Document, 10), 5, 5},
		{"empty query", "", make([]schema.Document, 5), 3, 5}, // preFilterBM25 returns docs if query is empty or less than 3 chars terms
		{"empty docs", "foo", []schema.Document{}, 3, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preFilterBM25(tt.query, tt.docs, tt.topK)
			if len(got) != tt.expectLen {
				t.Errorf("preFilterBM25() len = %d, want %d", len(got), tt.expectLen)
			}
		})
	}
}
