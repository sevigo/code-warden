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
		wantCount   int
		expectErr   bool
	}{
		{
			name: "Valid Review",
			input: `# REVIEW SUMMARY
This is a good PR.

# VERDICT
APPROVE

# SUGGESTIONS

## Suggestion [main.go:10]
**Severity:** High
**Category:** Logic

### Comment
Fix this bug.

### Rationale
It crashes.

### Fix
` + "```go" + `
if x != nil {
  // ...
}
` + "```",
			wantSummary: "This is a good PR.",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Multiple Suggestions",
			input: `# REVIEW SUMMARY
Summary

# SUGGESTIONS

## Suggestion [foo.go:1]
**Severity:** Low
### Comment
Comment 1

## Suggestion [bar.go:2]
**Severity:** Critical
### Comment
Comment 2
`,
			wantSummary: "Summary",
			wantCount:   2,
			expectErr:   false,
		},
		{
			name:      "Empty Input",
			input:     "",
			expectErr: true,
		},
		{
			name:        "Response Wrapped in Markdown Fence",
			input:       "```markdown\n# REVIEW SUMMARY\nLooks good.\n\n# VERDICT\nAPPROVE\n```",
			wantSummary: "Looks good.",
			wantCount:   0,
			expectErr:   false,
		},
		{
			name: "No Suggestions Section",
			input: `# REVIEW SUMMARY
Clean code, no issues found.

# VERDICT
APPROVE
`,
			wantSummary: "Clean code, no issues found.",
			wantCount:   0,
			expectErr:   false,
		},
		{
			name: "Suggestion With Whitespace Before Line Number",
			input: `# REVIEW SUMMARY
Review

# SUGGESTIONS

## Suggestion [src/app.ts: 42]
**Severity:** Medium
**Category:** Performance

### Comment
Avoid repeated allocations.
`,
			wantSummary: "Review",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Direct Header Format",
			input: `# REVIEW SUMMARY
Summary

# SUGGESTIONS

## pkg/file.go:55
**Severity:** Medium
### Comment
Issue here.
`,
			wantSummary: "Summary",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Multiline Suggestion",
			input: `# REVIEW SUMMARY
Summary

# SUGGESTIONS

## Suggestion [main.go:10-20]
**Severity:** Low
### Comment
Refactor this range.
`,
			wantSummary: "Summary",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Verdict does not leak into Summary",
			input: `# REVIEW SUMMARY
Good code.
# VERDICT
APPROVE`,
			wantSummary: "Good code.",
			wantCount:   0,
			expectErr:   false,
		},
		{
			name: "Suggestion With En Dash Range",
			input: `# REVIEW SUMMARY
Summary

# SUGGESTIONS

## Suggestion [main.go:10–20]
**Severity:** Critical
### Comment
Fix range with En Dash.
`,
			wantSummary: "Summary",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Suggestion Wrapped in Backticks",
			input: `# REVIEW SUMMARY
Summary

# SUGGESTIONS

## Suggestion ` + "`main.go:15`" + `
**Severity:** Medium
### Comment
Fix file wrapped in backticks.
`,
			wantSummary: "Summary",
			wantCount:   1,
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
			// Verdict should not be part of summary (checked by Contains above via wantSummary not having it)
			// But we can also check explicit exclusion if needed.
			if strings.Contains(tt.input, "# VERDICT") {
				// The input has verdict, check if parsed into field
				// We don't have expected verdict field in struct yet, but can check it's not in summary.
				assert.NotContains(t, got.Summary, "APPROVE")
				assert.NotEmpty(t, got.Verdict)
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
