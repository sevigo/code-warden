package llm

import (
	"strings"
	"sync"
	"testing"

	"github.com/sevigo/goframe/schema"

	internalgithub "github.com/sevigo/code-warden/internal/github"
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

func TestCleanCommentForQuery(t *testing.T) {
	r := &ragService{}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "removes markdown backticks",
			input:    "Check `variable` usage",
			expected: "Check variable usage",
		},
		{
			name:     "removes triple backticks",
			input:    "```go\nfunc main() {}\n```",
			expected: "go func main() {}",
		},
		{
			name:     "removes status markers",
			input:    "**Status:** UNRESOLVED - Fix the nil pointer",
			expected: "- Fix the nil pointer",
		},
		{
			name:     "normalizes observation markers",
			input:    "**Observation:** The code has a bug",
			expected: "| Observation: The code has a bug",
		},
		{
			name:     "normalizes root cause markers",
			input:    "**Root Cause:** Race condition detected",
			expected: "| Root Cause: Race condition detected",
		},
		{
			name:     "normalizes fix markers",
			input:    "**Fix:** Add mutex lock",
			expected: "| Fix: Add mutex lock",
		},
		{
			name:     "trims whitespace",
			input:    "  some content  ",
			expected: "some content",
		},
		{
			name:     "limits length to 500 chars",
			input:    strings.Repeat("a", 600),
			expected: strings.Repeat("a", 500),
		},
		{
			name:     "combined cleaning",
			input:    "**Status:** FIXED **Observation:** `code` issue **Fix:** add check",
			expected: "| Observation: code issue | Fix: add check",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.cleanCommentForQuery(tt.input)
			if got != tt.expected {
				t.Errorf("cleanCommentForQuery(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExtractCommentsFromReview(t *testing.T) {
	r := &ragService{}

	tests := []struct {
		name     string
		review   string
		expected int // expected number of queries
	}{
		{
			name:     "empty review",
			review:   "",
			expected: 0,
		},
		{
			name: "single suggestion with comment",
			review: `<suggestion>
	<file>test.go</file>
	<line>10</line>
	<comment>Fix the nil pointer dereference</comment>
</suggestion>`,
			expected: 1,
		},
		{
			name: "multiple suggestions",
			review: `<suggestion>
	<file>a.go</file>
	<line>1</line>
	<comment>First issue</comment>
</suggestion>
<suggestion>
	<file>b.go</file>
	<line>2</line>
	<comment>Second issue</comment>
</suggestion>`,
			expected: 2,
		},
		{
			name: "suggestion without comment tag",
			review: `<suggestion>
	<file>test.go</file>
	<line>10</line>
</suggestion>`,
			expected: 0,
		},
		{
			name: "empty comment",
			review: `<suggestion>
	<file>test.go</file>
	<line>10</line>
	<comment>   </comment>
</suggestion>`,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.extractCommentsFromReview(t.Context(), tt.review)
			if len(got) != tt.expected {
				t.Errorf("extractCommentsFromReview() returned %d queries, want %d", len(got), tt.expected)
			}
		})
	}
}

func TestCombineReReviewContext(t *testing.T) {
	r := &ragService{}

	tests := []struct {
		name           string
		standardCtx    string
		feedbackCtx    string
		expectedParts  []string
		unexpectedParts []string
	}{
		{
			name:           "both contexts present",
			standardCtx:    "Standard context",
			feedbackCtx:    "Feedback context",
			expectedParts:  []string{"Feedback context", "---", "Standard context"},
			unexpectedParts: []string{},
		},
		{
			name:           "only standard context",
			standardCtx:    "Standard context",
			feedbackCtx:    "",
			expectedParts:  []string{"Standard context"},
			unexpectedParts: []string{"---"},
		},
		{
			name:           "only feedback context",
			standardCtx:    "",
			feedbackCtx:    "Feedback context",
			expectedParts:  []string{"Feedback context", "---"},
			unexpectedParts: []string{},
		},
		{
			name:           "both empty",
			standardCtx:    "",
			feedbackCtx:    "",
			expectedParts:  []string{},
			unexpectedParts: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.combineReReviewContext(tt.standardCtx, tt.feedbackCtx)
			for _, part := range tt.expectedParts {
				if !strings.Contains(got, part) {
					t.Errorf("combineReReviewContext() missing expected part %q in result: %q", part, got)
				}
			}
			for _, part := range tt.unexpectedParts {
				if strings.Contains(got, part) {
					t.Errorf("combineReReviewContext() unexpectedly contains %q in result: %q", part, got)
				}
			}
		})
	}
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
