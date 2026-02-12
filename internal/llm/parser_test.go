package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMarkdownReview(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantSummary string
		wantVerdict string
		wantCount   int
		expectErr   bool
	}{
		{
			name: "Valid Review Traditional",
			input: `# REVIEW SUMMARY
This is a good PR.

# VERDICT
APPROVE

# SUGGESTIONS

## Suggestion [main.go:10]
**Severity:** High
**Category:** Logic

### Comment
Fix this bug.`,
			wantSummary: "This is a good PR.",
			wantVerdict: "APPROVE",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Valid Review New Format (Emojis)",
			input: `# 🛡️ CODE WARDEN CONSENSUS REVIEW

## 🚦 VERDICT
[APPROVE]

> **SUMMARY**
> This PR is excellent.

## 🔍 KEY FINDINGS

## **File:** ` + "`pkg/api.go:20`" + `
**Severity:** Medium
### Comment
Check input validation.`,
			wantSummary: "This PR is excellent.",
			wantVerdict: "APPROVE",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Verdict in Header",
			input: `
# REVIEW SUMMARY
LGTM

## 🚦 VERDICT: [REQUEST_CHANGES]

# SUGGESTIONS
`,
			wantSummary: "LGTM",
			wantVerdict: "REQUEST_CHANGES",
			wantCount:   0,
			expectErr:   false,
		},
		{
			name: "Verdict in Header without Brackets",
			input: `
# REVIEW SUMMARY
LGTM

## 🚦 VERDICT: APPROVE

# SUGGESTIONS
`,
			wantSummary: "LGTM",
			wantVerdict: "APPROVE",
			wantCount:   0,
			expectErr:   false,
		},
		{
			name: "False Positive Prevention (Comment inside Summary)",
			input: `# REVIEW SUMMARY
The user asked: "Should I add # VERDICT here?"
And I said no.

# VERDICT
COMMENT
`,
			wantSummary: `The user asked: "Should I add # VERDICT here?"
And I said no.`,
			wantVerdict: "COMMENT",
			wantCount:   0,
			expectErr:   false,
		},
		{
			name: "Multiple Suggestions New Format",
			input: `# SUMMARY
TLDR

# SUGGESTIONS

## **File:** ` + "`a.go:1`" + `
### Comment
A

## **File:** ` + "`b.go:2`" + `
### Comment
B
`,
			wantSummary: "TLDR",
			wantVerdict: "",
			wantCount:   2,
			expectErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMarkdownReview(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Contains(t, got.Summary, tt.wantSummary)
			if tt.wantVerdict != "" {
				assert.Equal(t, tt.wantVerdict, got.Verdict)
			}

			assert.Len(t, got.Suggestions, tt.wantCount)
			if tt.wantCount > 0 {
				assert.NotEmpty(t, got.Suggestions[0].FilePath)
				// For multiline test, verify start/end
				switch {
				case strings.Contains(tt.name, "Multiline") || strings.Contains(tt.name, "En Dash"):
					assert.Equal(t, 10, got.Suggestions[0].StartLine)
					assert.Equal(t, 20, got.Suggestions[0].LineNumber)
				case strings.Contains(tt.name, "Backticks"):
					assert.Equal(t, 15, got.Suggestions[0].LineNumber)
					assert.Equal(t, 15, got.Suggestions[0].StartLine)
				default:
					// For single line, StartLine MUST equal LineNumber
					assert.Equal(t, got.Suggestions[0].LineNumber, got.Suggestions[0].StartLine, "Single line suggestion should have StartLine == LineNumber")
				}
			}
		})
	}
}

func TestStripMarkdownFence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "No fence",
			input: "# Summary\nHello",
			want:  "# Summary\nHello",
		},
		{
			name:  "Markdown fence",
			input: "```markdown\n# Summary\nHello\n```",
			want:  "# Summary\nHello",
		},
		{
			name:  "MD fence",
			input: "```md\n# Summary\n```",
			want:  "# Summary",
		},
		{
			name:  "Not a markdown fence",
			input: "```json\n{}\n```",
			want:  "```json\n{}\n```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdownFence(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseMarkdownReview_TitleGap_And_BoldPath(t *testing.T) {
	input := `# REVIEW SUMMARY

This is a summary.

# SUGGESTIONS

#### 1. [Missing Title Issue]
**File:** ` + "`**internal/llm/parser.go**`" + `:123
**Observation:** Some observation.

### Comment
This is the comment.
`
	review, err := parseMarkdownReview(input)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(review.Suggestions) != 1 {
		t.Fatalf("Caught %d suggestions, want 1", len(review.Suggestions))
	}

	s := review.Suggestions[0]
	if s.FilePath != "internal/llm/parser.go" {
		t.Errorf("Got FilePath %q, want %q", s.FilePath, "internal/llm/parser.go")
	}

	// The title "#### 1. [Missing Title Issue]" should be captured in the comment/description
	if !strings.Contains(s.Comment, "[Missing Title Issue]") {
		t.Errorf("Comment missing title. Got:\n%s", s.Comment)
	}
}

func TestParseMarkdownReview_Chaos_Golden(t *testing.T) {
	input := `## 📝 Detailed Suggestions
### 🔴 Critical Bug
**File:** ` + "`**internal/llm/parser.go**`" + `:50
**Observation:** Logic is flawed.`

	review, err := parseMarkdownReview(input)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(review.Suggestions) != 1 {
		t.Fatalf("Caught %d suggestions, want 1", len(review.Suggestions))
	}

	s := review.Suggestions[0]
	if s.FilePath != "internal/llm/parser.go" {
		t.Errorf("Got FilePath %q, want %q", s.FilePath, "internal/llm/parser.go")
	}
	if !strings.Contains(s.Comment, "Critical Bug") {
		t.Errorf("Comment missing Critical Bug title. Got:\n%s", s.Comment)
	}
	if !strings.Contains(s.Comment, "Observation") {
		t.Errorf("Comment missing Observation. Got:\n%s", s.Comment)
	}
}

func TestParseMarkdownReview_FlexibleHeaders_And_Dashes(t *testing.T) {
	input := `## Summary
### Suggestion src/foo.go:10–20
**Severity:** Medium
Fix this en-dash range.

#### Suggestion [src/bar.go:30]
**Severity:** Low
Deep header level.
`

	review, err := parseMarkdownReview(input)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(review.Suggestions) != 2 {
		t.Fatalf("Caught %d suggestions, want 2", len(review.Suggestions))
	}

	// 1. Check En-dash handling
	s1 := review.Suggestions[0]
	if s1.FilePath != "src/foo.go" {
		t.Errorf("s1 file: got %q, want src/foo.go", s1.FilePath)
	}
	if s1.StartLine != 10 || s1.LineNumber != 20 {
		t.Errorf("s1 range: got %d-%d, want 10-20 (en-dash handling failed)", s1.StartLine, s1.LineNumber)
	}

	// 2. Check Flexible Header (#### Suggestion)
	s2 := review.Suggestions[1]
	if s2.FilePath != "src/bar.go" {
		t.Errorf("s2 file: got %q, want src/bar.go", s2.FilePath)
	}
	if s2.LineNumber != 30 {
		t.Errorf("s2 line: got %d, want 30", s2.LineNumber)
	}
}

func TestParseMarkdownReview_NonBoldBackticks(t *testing.T) {
	// Case: `internal/llm/parser.go:123` (Backticks ONLY, no stars)
	input := `## Suggestion ` + "`internal/llm/parser.go:123`" + `
**Severity:** Low
Fix ` + "`weird`" + ` formatting.`

	review, err := parseMarkdownReview(input)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(review.Suggestions) != 1 {
		t.Fatalf("Caught %d suggestions, want 1", len(review.Suggestions))
	}

	s := review.Suggestions[0]
	if s.FilePath != "internal/llm/parser.go" {
		t.Errorf("Got FilePath %q, want internal/llm/parser.go", s.FilePath)
	}
	if s.LineNumber != 123 {
		t.Errorf("Got LineNumber %d, want 123", s.LineNumber)
	}
}
