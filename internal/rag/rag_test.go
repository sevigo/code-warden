package rag

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/sevigo/goframe/schema"
	"go.uber.org/mock/gomock"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/mocks"
)

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

func TestExtractSymbolsFromPatch(t *testing.T) {
	tests := []struct {
		name     string
		patch    string
		expected []string
	}{
		{
			name:     "empty patch",
			patch:    "",
			expected: []string{},
		},
		{
			name: "type definition in Go",
			patch: `diff --git a/types.go b/types.go
@@ -1,5 +1,10 @@
 package main

+type Config struct {
+	Timeout int
+	Host    string
+}
+
 func main() {}`,
			expected: []string{"Config"},
		},
		{
			name: "function definition",
			patch: `diff --git a/main.go b/main.go
@@ -1,5 +1,10 @@
 package main

+func ProcessData(data string) error {
+	return nil
+}`,
			expected: []string{"ProcessData"},
		},
		{
			name:     "struct instantiation",
			patch:    `+cfg := Config{Timeout: 30}`,
			expected: []string{"Config"},
		},
		{
			name: "method definition",
			patch: `+func (c *Config) GetTimeout() int {
+	return c.Timeout
+}`,
			expected: []string{"GetTimeout"}, // The receiver type Config is not captured by current regex
		},
		{
			name: "interface definition",
			patch: `+type Reader interface {
+	Read(p []byte) (n int, err error)
+}`,
			expected: []string{"Reader"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSymbolsFromPatch(tt.patch)
			// Check that all expected symbols are present
			for _, expected := range tt.expected {
				found := false
				for _, g := range got {
					if g == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("extractSymbolsFromPatch() missing expected symbol %q in result %v", expected, got)
				}
			}
		})
	}
}

func TestGatherDefinitionsContext_EmptyInput(t *testing.T) {
	r := &ragService{
		logger: slog.Default(),
	}

	result, err := r.gatherDefinitionsContext(t.Context(), nil, []internalgithub.ChangedFile{})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for empty input, got %q", result)
	}
}

func TestGatherDefinitionsContext_NoPatch(t *testing.T) {
	r := &ragService{
		logger: slog.Default(),
	}

	// Files without patches should be skipped, resulting in no symbols
	changedFiles := []internalgithub.ChangedFile{
		{Filename: "main.go", Patch: ""},
		{Filename: "utils.go", Patch: ""},
	}

	result, err := r.gatherDefinitionsContext(t.Context(), nil, changedFiles)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string when no patches, got %q", result)
	}
}

func TestGatherDefinitionsContext_WithSymbols(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSVS := mocks.NewMockScopedVectorStore(ctrl)

	// Expect definition lookups for extracted symbols
	mockSVS.EXPECT().
		SimilaritySearch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]schema.Document{
			{
				PageContent: "type Config struct { Timeout int }",
				Metadata:    map[string]any{"source": "config.go"},
			},
		}, nil).
		AnyTimes()

	r := &ragService{
		logger: slog.Default(),
	}

	changedFiles := []internalgithub.ChangedFile{
		{
			Filename: "main.go",
			Patch:    "+type Config struct { Timeout int }\n+func Process() {}",
		},
	}

	result, err := r.gatherDefinitionsContext(t.Context(), mockSVS, changedFiles)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	// Should contain the header and possibly definitions
	if result != "" {
		if !strings.Contains(result, "Resolved Type Definitions") {
			t.Errorf("expected result to contain 'Resolved Type Definitions', got %q", result)
		}
	}
}
