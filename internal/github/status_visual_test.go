package github

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

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

func TestFormatInlineComment_NoStrip(t *testing.T) {
	// Test that we normalize >> to > (based on user feedback)
	// And that we wrap in alert.
	sug := core.Suggestion{
		FilePath:   "main.go",
		LineNumber: 10,
		Severity:   "High",
		Comment:    "> This is a blockquote.\n>> This is nested.",
	}

	output := formatInlineComment(sug)
	// Expect the output to be wrapped in a Warning alert
	assert.Contains(t, output, "> [!WARNING]")

	// Let's assert what the code DOES (User's suggested fix implies loss of nesting but valid GitHub rendering)
	// Input: > This is a blockquote. -> Output: > This is a blockquote. (Text inside alert)
	// Input: >> This is nested. -> Output: > This is nested. (Text inside alert)

	// We check that the output contains the lines prefixed by > (inside alert)
	assert.Contains(t, output, "> This is a blockquote.")
	assert.Contains(t, output, "> This is nested.")

	// Ensure we don't have the ">>" prefix which breaks GitHub
	assert.NotContains(t, output, ">> This is nested.")
}

func TestFormatInlineComment_Visual(t *testing.T) {
	// 1. Basic Comment
	sug := core.Suggestion{
		FilePath:   "main.go",
		LineNumber: 42,
		Severity:   "Critical",
		Category:   "Security",
		Comment:    "### SQL Injection Vulnerability\n\nThis line is vulnerable to SQL injection.\n\n```go\nquery := fmt.Sprintf(\"SELECT * FROM users WHERE name = '%s'\", name)\n```",
	}

	output := formatInlineComment(sug)

	// Check Header
	assert.Contains(t, output, "### 🔴 Critical | Security | SQL Injection Vulnerability")
	// Check Alert Wrapper (Critical -> CAUTION)
	assert.Contains(t, output, "> [!CAUTION]")
	// Check content inside alert
	assert.Contains(t, output, "> This line is vulnerable to SQL injection.")
	// Check Code Block inside alert (simple Line + \n)
	assert.Contains(t, output, "```go")
}

func TestFormatReviewSummary_Visual(t *testing.T) {
	review := &core.StructuredReview{
		Verdict: "REQUEST_CHANGES",
		Summary: "This is the main summary text.",
		Suggestions: []core.Suggestion{
			{Severity: "Critical", Comment: "Fix this now"},
			{Severity: "High", Comment: "Make it better"},
			{Severity: "Low", Comment: "Nitpick"},
		},
	}

	output := formatReviewSummary(review)

	// Check for Verdict
	assert.Contains(t, output, "🚫 Verdict: REQUEST_CHANGES")
	// Check for Summary
	assert.Contains(t, output, "This is the main summary text.")
	// Check for Table Header
	assert.Contains(t, output, "#### 📊 Issue Statistics")
	// Check for correct rows and icons
	assert.Contains(t, output, "| 🔴 Critical | 1 |")
	assert.Contains(t, output, "| 🟠 High | 1 |")
	// Medium is 0, should not be present
	assert.NotContains(t, output, "| 🟡 Medium |")
	assert.Contains(t, output, "| 🟢 Low | 1 |")
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

func TestFormatReviewSummary_WithRatings(t *testing.T) {
	review := &core.StructuredReview{
		Verdict: "APPROVE",
		Summary: "Overall good.",
		Suggestions: []core.Suggestion{
			{Severity: "Low", Comment: "Nit"},
		},
		ModelRatings: []core.ModelRating{
			{ModelName: "gpt-4o", Score: 5, Critique: "Excellent"},
			{ModelName: "claude-3-opus", Score: 4, Critique: "Good"},
		},
	}

	output := formatReviewSummary(review)

	assert.Contains(t, output, "### ✅ Verdict: APPROVE")
	assert.Contains(t, output, "#### 🤖 Model Ratings")
	assert.Contains(t, output, "| gpt-4o | ⭐⭐⭐⭐⭐ | Excellent |")
	assert.Contains(t, output, "| claude-3-opus | ⭐⭐⭐⭐ | Good |")
}
