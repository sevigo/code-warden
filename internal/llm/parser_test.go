package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			name: "Valid XML Review",
			input: `
<review>
  <verdict>APPROVE</verdict>
  <summary>This is a good PR.</summary>
  <suggestions>
    <suggestion>
      <file>main.go</file>
      <line>10</line>
      <severity>High</severity>
      <category>Logic</category>
      <confidence>90</confidence>
      <estimated_fix_time>15m</estimated_fix_time>
      <reproducibility>Always</reproducibility>
      <comment>Fix this bug.</comment>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "This is a good PR.",
			wantVerdict: "APPROVE",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Preamble-Resilient XML",
			input: `
Some preamble text before the review.
Maybe some technical context.

<review>
  <verdict>REQUEST_CHANGES</verdict>
  <summary>PR has issues.</summary>
  <suggestions>
    <suggestion>
      <file>pkg/api.go</file>
      <line>20-25</line>
      <severity>Medium</severity>
      <comment>Check input validation.</comment>
    </suggestion>
  </suggestions>
</review>

Some postamble text.`,
			wantSummary: "PR has issues.",
			wantVerdict: "REQUEST_CHANGES",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Dirty XML (Bolded Path and Extra Tags)",
			input: `
<review>
  <verdict>[APPROVE]</verdict>
  <summary>Summary with <b>tags</b></summary>
  <suggestions>
    <suggestion>
      <file>**` + "`path/to/file.go`" + `**</file>
      <line>123</line>
      <comment>### Issue Title
Rationale: ...</comment>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "Summary with <b>tags</b>",
			wantVerdict: "APPROVE",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Multiple Suggestions and Range",
			input: `
<review>
  <suggestions>
    <suggestion>
      <file>a.go</file>
      <line>1</line>
      <comment>A</comment>
    </suggestion>
    <suggestion>
      <file>b.go</file>
      <line>10-20</line>
      <comment>B</comment>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "",
			wantVerdict: "",
			wantCount:   2,
			expectErr:   false,
		},
		{
			name:      "Missing Review Tag",
			input:     "This is just plain text without tags.",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMarkdownReview(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, got.Summary, tt.wantSummary)
			if tt.wantVerdict != "" {
				assert.Equal(t, tt.wantVerdict, got.Verdict, "Verdict mismatch")
			}

			assert.Len(t, got.Suggestions, tt.wantCount)
			if tt.wantCount > 0 && len(got.Suggestions) > 0 {
				assert.NotEmpty(t, got.Suggestions[0].FilePath)
				if tt.name == "Valid XML Review" {
					assert.Equal(t, 90, got.Suggestions[0].Confidence)
					assert.Equal(t, "15m", got.Suggestions[0].EstimatedFixTime)
					assert.Equal(t, "Always", got.Suggestions[0].Reproducibility)
				}
				if tt.name == "Dirty XML (Bolded Path and Extra Tags)" {
					assert.Equal(t, "path/to/file.go", got.Suggestions[0].FilePath)
				}
				if strings.Contains(tt.name, "Range") {
					assert.Equal(t, 10, got.Suggestions[1].StartLine)
					assert.Equal(t, 20, got.Suggestions[1].LineNumber)
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
			input: "<review>Hello</review>",
			want:  "<review>Hello</review>",
		},
		{
			name:  "Markdown fence",
			input: "```xml\n<review>\nHello\n</review>\n```",
			want:  "<review>\nHello\n</review>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdownFence(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
