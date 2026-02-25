package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
)

func TestParseLegacyMarkdownReview(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantSummary string
		wantVerdict string
		wantCount   int
		expectErr   bool
	}{
		{
			name: "Legacy Markdown Review",
			input: `
# REVIEW SUMMARY
Great PR, but fix the typo.

# SUGGESTIONS
*   **File:** path/to/legacy.go:42
    **Severity:** Medium
    Follow the naming convention.`,
			wantSummary: "Great PR, but fix the typo.",
			wantVerdict: "",
			wantCount:   1,
			expectErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLegacyMarkdownReview(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			verifyReviewResults(t, tt.name, got, tt.wantSummary, tt.wantVerdict, tt.wantCount)
		})
	}
}

func verifyReviewResults(t *testing.T, name string, got *core.StructuredReview, wantSummary, wantVerdict string, wantCount int) {
	assert.Contains(t, got.Summary, wantSummary)
	if wantVerdict != "" {
		assert.Equal(t, wantVerdict, got.Verdict, "Verdict mismatch")
	}
	assert.Len(t, got.Suggestions, wantCount)

	if wantCount == 0 || len(got.Suggestions) == 0 {
		return
	}

	s := got.Suggestions[0]
	assert.NotEmpty(t, s.FilePath)

	verifySpecificMetadata(t, name, got)
	verifyLineRanges(t, name, got)
	verifyCodeSuggestion(t, name, got)
}

func verifyCodeSuggestion(t *testing.T, name string, got *core.StructuredReview) {
	if !strings.Contains(name, "Code Suggestion") {
		return
	}
	s := got.Suggestions[0]
	expectedCode := "func main() {\n\tfmt.Println(\"Hello\")\n}"
	assert.Equal(t, expectedCode, s.CodeSuggestion)
}

func verifySpecificMetadata(t *testing.T, name string, got *core.StructuredReview) {
	s := got.Suggestions[0]
	if name == "Valid XML Review" {
		assert.Equal(t, 90, s.Confidence)
		assert.Equal(t, "15m", s.EstimatedFixTime)
		assert.Equal(t, "Always", s.Reproducibility)
	}
	if name == "Dirty XML (Bolded Path and Extra Tags)" {
		assert.Equal(t, "path/to/file.go", s.FilePath)
	}
	if name == "Comment Tag Stripping" {
		assert.Equal(t, "Fix this.", s.Comment)
	}
}

func verifyLineRanges(t *testing.T, name string, got *core.StructuredReview) {
	if !strings.Contains(name, "Range") && !strings.Contains(name, "Dashes") {
		return
	}

	idx := 0
	if name == "Multiple Suggestions and Range" {
		idx = 1
	}

	assert.Equal(t, 10, got.Suggestions[idx].StartLine)
	if strings.Contains(name, "Dashes") {
		assert.Equal(t, 20, got.Suggestions[0].LineNumber)
		assert.Equal(t, 30, got.Suggestions[1].StartLine)
		assert.Equal(t, 40, got.Suggestions[1].LineNumber)
	} else {
		assert.Equal(t, 20, got.Suggestions[idx].LineNumber)
	}
}
