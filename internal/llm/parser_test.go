package llm

import (
	"reflect"
	"testing"

	"github.com/sevigo/code-warden/internal/core"
)

func TestParseMarkdownReview(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *core.StructuredReview
		wantErr bool
	}{
		{
			name: "Standard Review",
			input: `
# REVIEW SUMMARY
This is a summary.
Multilines supported.

# VERDICT
REQUEST_CHANGES

# SUGGESTIONS

## Suggestion internal/main.go:10
**Severity:** High
**Category:** Logic

### Comment
This is a comment.

## Suggestion cmd/cli.go:20
**Severity:** Low
**Category:** Style

### Comment
Another comment.
`,
			want: &core.StructuredReview{
				Summary: "This is a summary.\nMultilines supported.",
				Verdict: "REQUEST_CHANGES",
				Suggestions: []core.Suggestion{
					{
						FilePath:   "internal/main.go",
						LineNumber: 10,
						Severity:   "High",
						Category:   "Logic",
						Comment:    "This is a comment.",
					},
					{
						FilePath:   "cmd/cli.go",
						LineNumber: 20,
						Severity:   "Low",
						Category:   "Style",
						Comment:    "Another comment.",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Missing Verdict",
			input: `# REVIEW SUMMARY
Summary only.`,
			want: &core.StructuredReview{
				Summary:     "Summary only.",
				Verdict:     "",
				Suggestions: nil,
			},
			wantErr: false,
		},
		{
			name: "Flexible Suggestion Header",
			input: `
# REVIEW SUMMARY
Summary.

# VERDICT
APPROVE

# SUGGESTIONS

## Suggestion path/to/file.go:123
**Severity:** Medium
**Category:** Logic

### Comment
Comment.
`,
			want: &core.StructuredReview{
				Summary: "Summary.",
				Verdict: "APPROVE",
				Suggestions: []core.Suggestion{
					{
						FilePath:   "path/to/file.go",
						LineNumber: 123,
						Severity:   "Medium",
						Category:   "Logic",
						Comment:    "Comment.",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Code Block in Comment",
			input: `
# REVIEW SUMMARY
Summary.

# VERDICT
APPROVE

# SUGGESTIONS

## Suggestion file.go:1
**Severity:** Low
**Category:** Style

### Comment
Check this code:
` + "```go" + `
fmt.Println("Hello")
` + "```" + `
`,
			want: &core.StructuredReview{
				Summary: "Summary.",
				Verdict: "APPROVE",
				Suggestions: []core.Suggestion{
					{
						FilePath:   "file.go",
						LineNumber: 1,
						Severity:   "Low",
						Category:   "Style",
						Comment:    "Check this code:\n```go\nfmt.Println(\"Hello\")\n```",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMarkdownReview(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMarkdownReview() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseMarkdownReview() = %v, want %v", got, tt.want)
			}
		})
	}
}
