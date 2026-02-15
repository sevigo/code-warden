package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sevigo/code-warden/internal/core"
)

func TestFormatInlineComment(t *testing.T) {
	tests := []struct {
		name     string
		sug      core.Suggestion
		contains []string
		excludes []string
	}{
		{
			name: "critical severity uses GitHub alert",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "Critical",
				Category:   "Security",
				Comment:    "This is a security issue that needs immediate attention.",
			},
			contains: []string{
				"**🔴 Critical** — Security",
				"> [!CAUTION]",
				"This is a security issue",
			},
			excludes: []string{
				"### 🛡️",
				"| _Security_",
			},
		},
		{
			name: "high severity uses GitHub alert",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "High",
				Category:   "Bug",
				Comment:    "Potential nil pointer dereference.",
			},
			contains: []string{
				"**🟠 High** — Bug",
				"> [!WARNING]",
			},
			excludes: []string{
				"### 🛡️",
			},
		},
		{
			name: "medium severity uses plain markdown (no alert)",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "Medium",
				Category:   "Style",
				Comment:    "Consider using a more descriptive variable name.",
			},
			contains: []string{
				"**🟡 Medium** — Style",
				"Consider using a more descriptive variable name.",
			},
			excludes: []string{
				"> [!IMPORTANT]",
				"> [!NOTE]",
				"> Consider",
			},
		},
		{
			name: "low severity uses plain markdown (no alert)",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "Low",
				Comment:    "Minor typo in comment.",
			},
			contains: []string{
				"**🟢 Low**",
				"Minor typo in comment.",
			},
			excludes: []string{
				"> [!NOTE]",
				"> Minor",
			},
		},
		{
			name: "code block stays outside alert",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "High",
				Comment:    "Check this out:\n\n```go\nfunc hello() {\n    fmt.Println(\"hi\")\n}\n```",
			},
			contains: []string{
				"**🟠 High**",
				"> [!WARNING]",
				"```go",
				"fmt.Println",
			},
			excludes: []string{
				"> ```go",
				">     fmt.Println",
			},
		},
		{
			name: "strips legacy ### title header",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "Critical",
				Comment:    "### Old Style Title\n\nThis is the content.",
			},
			contains: []string{
				"**🔴 Critical**",
				"> [!CAUTION]",
				"This is the content.",
			},
			excludes: []string{
				"### Old Style Title",
				"### 🛡️",
			},
		},
		{
			name: "converts #### header to bold",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "High",
				Comment:    "Problem found.\n\n#### Suggested Fix\nApply this change.",
			},
			contains: []string{
				"**🟠 High**",
				"💡 **Fix:**",
				"Apply this change.",
			},
			excludes: []string{
				"#### Suggested Fix",
			},
		},
		{
			name: "empty comment returns empty string",
			sug: core.Suggestion{
				FilePath: "test.go", LineNumber: 10, Severity: "High", Comment: "",
			},
			contains: []string{""},
		},
		{
			name: "invalid line number returns empty string",
			sug: core.Suggestion{
				FilePath: "test.go", LineNumber: 0, Severity: "Medium",
				Comment: "Fix this",
			},
			contains: []string{""},
		},
		{
			name: "no category still works",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "High",
				Comment:    "Issue without category.",
			},
			contains: []string{
				"**🟠 High**\n\n",
				"> [!WARNING]",
			},
			excludes: []string{
				" — ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatInlineComment(tt.sug)
			for _, c := range tt.contains {
				assert.Contains(t, got, c, "expected to contain: %s", c)
			}
			for _, e := range tt.excludes {
				assert.NotContains(t, got, e, "expected NOT to contain: %s", e)
			}
		})
	}
}

func TestFormatReviewSummary(t *testing.T) {
	tests := []struct {
		name     string
		review   *core.StructuredReview
		contains []string
		excludes []string
	}{
		{
			name: "summary with suggestions shows compact stats",
			review: &core.StructuredReview{
				Title:   "Test Review",
				Verdict: "APPROVE",
				Summary: "The code looks good overall.",
				Suggestions: []core.Suggestion{
					{Severity: "Critical"},
					{Severity: "Critical"},
					{Severity: "High"},
					{Severity: "Medium"},
				},
			},
			contains: []string{
				"## Test Review",
				"### ✅ Verdict: APPROVE",
				"The code looks good overall.",
				"*Found 4 suggestion(s):",
				"🔴 2 Critical",
				"🟠 1 High",
				"🟡 1 Medium",
			},
			excludes: []string{
				"### 📊 Issue Statistics",
				"| Severity | Count |",
			},
		},
		{
			name: "summary with only critical shows only critical",
			review: &core.StructuredReview{
				Verdict: "REQUEST_CHANGES",
				Summary: "Critical issues found.",
				Suggestions: []core.Suggestion{
					{Severity: "Critical"},
				},
			},
			contains: []string{
				"### 🚫 Verdict: REQUEST_CHANGES",
				"*Found 1 suggestion(s): 🔴 1 Critical*",
			},
			excludes: []string{
				"High",
				"Medium",
			},
		},
		{
			name: "summary with no suggestions has no stats",
			review: &core.StructuredReview{
				Verdict:     "APPROVE",
				Summary:     "No issues found.",
				Suggestions: []core.Suggestion{},
			},
			contains: []string{
				"## 🔍 Code Review Summary",
				"### ✅ Verdict: APPROVE",
				"No issues found.",
			},
			excludes: []string{
				"Found",
				"suggestion",
			},
		},
		{
			name: "default title when no title provided",
			review: &core.StructuredReview{
				Verdict: "COMMENT",
				Summary: "Some observations.",
				Suggestions: []core.Suggestion{
					{Severity: "Low"},
				},
			},
			contains: []string{
				"## 🔍 Code Review Summary",
				"### 💬 Verdict: COMMENT",
				"🟢 1 Low",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatReviewSummary(tt.review)
			for _, c := range tt.contains {
				assert.Contains(t, got, c, "expected to contain: %s", c)
			}
			for _, e := range tt.excludes {
				assert.NotContains(t, got, e, "expected NOT to contain: %s", e)
			}
		})
	}
}

func TestSeverityEmoji(t *testing.T) {
	tests := []struct {
		severity string
		expected string
	}{
		{"Critical", "🔴"},
		{"High", "🟠"},
		{"Medium", "🟡"},
		{"Low", "🟢"},
		{"Unknown", "⚪"},
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			got := severityEmoji(tt.severity)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestVerdictIcon(t *testing.T) {
	tests := []struct {
		verdict  string
		expected string
	}{
		{"APPROVE", "✅"},
		{"REQUEST_CHANGES", "🚫"},
		{"REQUEST CHANGES", "🚫"},
		{"COMMENT", "💬"},
		{"unknown", "📝"},
	}

	for _, tt := range tests {
		t.Run(tt.verdict, func(t *testing.T) {
			got := verdictIcon(tt.verdict)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestSeverityAlert(t *testing.T) {
	tests := []struct {
		severity string
		expected string
	}{
		{"Critical", "CAUTION"},
		{"High", "WARNING"},
		{"Medium", "IMPORTANT"},
		{"Low", "NOTE"},
		{"Unknown", "NOTE"},
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			got := severityAlert(tt.severity)
			assert.Equal(t, tt.expected, got)
		})
	}
}
