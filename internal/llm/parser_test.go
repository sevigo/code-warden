package llm

import (
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
			wantSummary: "This is a good PR.\n\n**Verdict:** APPROVE",
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
			wantSummary: "Looks good.\n\n**Verdict:** APPROVE",
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
			wantSummary: "Clean code, no issues found.\n\n**Verdict:** APPROVE",
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
			assert.Len(t, got.Suggestions, tt.wantCount)
			if tt.wantCount > 0 {
				assert.NotEmpty(t, got.Suggestions[0].FilePath)
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
