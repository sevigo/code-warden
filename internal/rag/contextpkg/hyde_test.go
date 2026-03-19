package contextpkg

import (
	"log/slog"
	"testing"

	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
)

func TestLanguageFromFilename(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"internal/rag/service.go", "Go"},
		{"src/App.tsx", "TypeScript (React)"},
		{"src/utils.ts", "TypeScript"},
		{"scripts/deploy.py", "Python"},
		{"app/Main.java", "Java"},
		{"src/lib.rs", "Rust"},
		{"main.c", "C"},
		{"include/header.h", "C"},
		{"src/main.cpp", "C++"},
		{"src/main.cc", "C++"},
		{"App.cs", "C#"},
		{"Main.kt", "Kotlin"},
		{"App.swift", "Swift"},
		{"unknown_file", "unknown"},
		{"config.yaml", "yaml"}, // fallback: strips dot
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := languageFromFilename(tt.filename)
			assert.Equal(t, tt.want, got)
		})
	}
}

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

// simpleCache is a minimal Cache implementation for tests.
type simpleCache struct {
	m map[string]any
}

func (c *simpleCache) Load(key string) (any, bool) {
	v, ok := c.m[key]
	return v, ok
}

func (c *simpleCache) Store(key string, value any) {
	c.m[key] = value
}

// TestGenerateHyDESnippetForFile_CacheHit verifies that a pre-populated cache
// entry is returned without calling the LLM.
func TestGenerateHyDESnippetForFile_CacheHit(t *testing.T) {
	b := &builderImpl{cfg: Config{Logger: slog.Default()}}

	cache := &simpleCache{m: make(map[string]any)}
	b.cfg.HyDECache = cache

	patch := "+func Process() error { return nil }"
	filePath := "internal/service.go"
	cacheKey := b.hashPatch(filePath + ":" + patch)
	cache.Store(cacheKey, "cached hypothetical snippet")

	// GeneratorLLM is nil — if the cache miss path were taken, this would panic.
	result, err := b.generateHyDESnippetForFile(t.Context(), patch, filePath, "Go")

	assert.NoError(t, err)
	assert.Equal(t, "cached hypothetical snippet", result)
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
