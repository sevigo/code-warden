package llm

import (
	"strings"
	"sync"
	"testing"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/goframe/schema"
)

func TestSanitizeModelForFilename(t *testing.T) {
	tests := []struct {
		input      string
		wantPrefix string
	}{
		{"kimi-k2.5:cloud", "kimi-k2.5_cloud_"},
		{"deepseek/v3", "deepseek_v3_"},
		{"suspicious..name", "suspicious..name_"},
		{"<invalid>", "invalid_"},
		{"COM1", "safe_COM1_"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeModelForFilename(tt.input)
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("SanitizeModelForFilename(%q) = %q, want prefix %q", tt.input, got, tt.wantPrefix)
			}
			// Check if hash part is exactly 16 hex chars (plus underscore)
			hashPart := got[len(tt.wantPrefix):]
			if len(hashPart) != 16 {
				t.Errorf("SanitizeModelForFilename(%q) hash part %q length = %d, want 16", tt.input, hashPart, len(hashPart))
			}
		})
	}

	t.Run("CollisionResistance", func(t *testing.T) {
		m1 := SanitizeModelForFilename("model:v1")
		m2 := SanitizeModelForFilename("model/v1")
		if m1 == m2 {
			t.Errorf("Collision detected: %q and %q both sanitize to %q", "model:v1", "model/v1", m1)
		}
	})
}

func TestProcessRelatedSnippet_Concurrency(t *testing.T) {
	r := &ragService{
		logger: nil, // Should handle nil logger gracefully in tests if using r.logger or we can mock it
	}
	// In reality we should use a real logger or mock, but let's assume it's fine for now
	// or initialize a dummy logger if needed.

	seenDocs := make(map[string]struct{})
	var mu sync.RWMutex
	var wg sync.WaitGroup

	doc := schema.Document{
		PageContent: "some content",
		Metadata:    map[string]any{"source": "file.go"},
	}
	file := internalgithub.ChangedFile{Filename: "file.go"}

	// Launch many goroutines to try and trigger a race on seenDocs
	for i := range 100 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			var builder strings.Builder
			r.processRelatedSnippet(doc, file, idx, seenDocs, &mu, []string{}, &builder)
		}(i)
	}
	wg.Wait()

	if len(seenDocs) != 1 {
		t.Errorf("expected 1 seen doc, got %d", len(seenDocs))
	}
}
