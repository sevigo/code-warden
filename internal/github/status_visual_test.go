package github

import (
	"strings"
	"testing"

	"github.com/sevigo/code-warden/internal/core"
)

func TestFormatInlineComment_Compact(t *testing.T) {
	tests := []struct {
		name     string
		sug      core.Suggestion
		contains []string
	}{
		{
			name: "Critical Security Issue with Title",
			sug: core.Suggestion{
				FilePath:   "main.go",
				LineNumber: 10,
				Severity:   "Critical",
				Category:   "Security",
				Comment:    "### SQL Injection Vulnerability\n\nUser input is concatenated directly into the query string.\n\n#### Recommendation\nUse parameterized queries.",
			},
			contains: []string{
				"### 🔴 Critical | Security | SQL Injection Vulnerability",
				"> [!CAUTION]",
				"> User input is concatenated directly into the query string.",
				"**Recommendation**",
				"Use parameterized queries.",
			},
		},
		{
			name: "Medium Style Issue without Title",
			sug: core.Suggestion{
				FilePath:   "utils.go",
				LineNumber: 5,
				Severity:   "Medium",
				Category:   "Style",
				Comment:    "Variable name `x` is too short.",
			},
			contains: []string{
				"### 🟡 Medium | Style | Code Review Finding",
				"> [!IMPORTANT]",
				"> Variable name `x` is too short.",
			},
		},
		{
			name: "Code Block Handling",
			sug: core.Suggestion{
				FilePath:   "api.go",
				LineNumber: 20,
				Severity:   "Low",
				Category:   "Refactor",
				Comment:    "Consider using `errors.New`:\n```go\nreturn errors.New(\"error\")\n```",
			},
			contains: []string{
				"### 🟢 Low | Refactor | Code Review Finding",
				"> [!NOTE]",
				"> Consider using `errors.New`:",
				"```go",
				"return errors.New(\"error\")",
				"```",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatInlineComment(tt.sug)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("formatInlineComment() missing %q\nGot:\n%s", want, got)
				}
			}
		})
	}
}

func TestFormatReviewSummary_Compact(t *testing.T) {
	review := &core.StructuredReview{
		Verdict: "REQUEST_CHANGES",
		Summary: "Several critical issues found.",
		Suggestions: []core.Suggestion{
			{Severity: "Critical"},
			{Severity: "Critical"},
			{Severity: "Medium"},
		},
	}

	got := formatReviewSummary(review)

	expected := []string{
		"### 🚫 Verdict: REQUEST_CHANGES",
		"Several critical issues found.",
		"#### 📊 Issue Statistics",
		"| 🔴 Critical | 2 |",
		"| 🟡 Medium | 1 |",
	}

	for _, want := range expected {
		if !strings.Contains(got, want) {
			t.Errorf("formatReviewSummary() missing %q\nGot:\n%s", want, got)
		}
	}
}
