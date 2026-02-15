package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
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
			name: "Indented XML (Pretty Printed)",
			input: `
<review>
  <summary>
    # Title
    This is indented.
  </summary>
  <suggestions>
    <suggestion>
      <file>main.go</file>
      <line>10</line>
      <comment>
        The comment is also indented.
        - Point 1
      </comment>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "# Title\nThis is indented.",
			wantVerdict: "",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "En and Em Dashes in Range",
			input: `
<review>
  <suggestions>
    <suggestion>
      <file>a.go</file>
      <line>10–20</line>
      <comment>En dash</comment>
    </suggestion>
    <suggestion>
      <file>b.go</file>
      <line>30—40</line>
      <comment>Em dash</comment>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "",
			wantVerdict: "",
			wantCount:   2,
			expectErr:   false,
		},
		{
			name:        "Tags with whitespace",
			input:       "<review ><summary >OK</summary   ></review >",
			wantSummary: "OK",
			wantCount:   0,
			expectErr:   false,
		},
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
		{
			name: "Absolute Path Rejection",
			input: `
<review>
  <summary>Path rejection test</summary>
  <suggestions>
    <suggestion>
      <file>/etc/passwd</file>
      <line>1</line>
      <comment>Should be dropped</comment>
    </suggestion>
    <suggestion>
      <file>../secrets.yaml</file>
      <line>1</line>
      <comment>Should also be dropped</comment>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "Path rejection test",
			wantCount:   0,
			expectErr:   false,
		},
		{
			name: "Windows Absolute Path Rejection",
			input: `
<review>
  <summary>Windows path rejection test</summary>
  <suggestions>
    <suggestion>
      <file>C:\windows\system32\config</file>
      <line>1</line>
      <comment>Should be dropped</comment>
    </suggestion>
    <suggestion>
      <file>\\server\share\file</file>
      <line>1</line>
      <comment>Should also be dropped</comment>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "Windows path rejection test",
			wantCount:   0,
			expectErr:   false,
		},
		{
			name:      "Missing Review Tag",
			input:     "This is just plain text without tags.",
			expectErr: true,
		},

		{
			name: "Explicit Code Suggestion",
			input: `
<review>
  <suggestions>
    <suggestion>
      <file>main.go</file>
      <line>10</line>
      <code_suggestion>
func main() {
	fmt.Println("Hello")
}
      </code_suggestion>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "",
			wantVerdict: "",
			wantCount:   1,
			expectErr:   false,
		},
		{
			name: "Explicit Code Suggestion with Markdown Fence",
			input: `
<review>
  <suggestions>
    <suggestion>
      <file>main.go</file>
      <line>10</line>
      <code_suggestion>
` + "```go" + `
func main() {
	fmt.Println("Hello")
}
` + "```" + `
      </code_suggestion>
    </suggestion>
  </suggestions>
</review>`,
			wantSummary: "",
			wantVerdict: "",
			wantCount:   1,
			expectErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMarkdownReview(tt.input)
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
			// Wait, implementation: return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
			// strings.TrimSpace will remove trailing \n. So no newline at end.
			// Let's check implementation again.
			// 438: 	return strings.Join(lines[start:end], "\n")
			// It joins with \n. It does NOT trim space at the very end of the result, but it trimmed space at start.
			// Actually:
			// 418: 	trimmed := strings.TrimSpace(s)
			// ...
			// lines := strings.Split(trimmed, "\n")
			// ...
			// return strings.Join(lines[start:end], "\n")

			// If input is "```xml\n<review>\nHello\n</review>\n```"
			// trimmed is same.
			// lines: ["```xml", "<review>", "Hello", "</review>", "```"]
			// start=1, end=4. lines[1:4] is ["<review>", "Hello", "</review>"]
			// result: "<review>\nHello\n</review>"
			// So NO newline at end.
		},
		{
			name:  "Unclosed fence",
			input: "```go\nfunc foo() {}",
			want:  "func foo() {}",
		},
		{
			name:  "Fence with whitespace",
			input: "   ```go   \nfunc foo() {}\n   ```   ",
			want:  "func foo() {}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripMarkdownFence(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
