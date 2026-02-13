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
			name: "code block stays outside alert",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "High",
				Comment:    "Check this out:\n\n```go\nfunc hello() {\n    fmt.Println(\"hi\")\n}\n```",
			},
			contains: []string{
				"> [!WARNING]",
				"```go",
				"    fmt.Println",
			},
			excludes: []string{
				"> ```go",
				">     fmt.Println",
			},
		},
		{
			name: "multiple alerts and content",
			sug: core.Suggestion{
				FilePath:   "test.go",
				LineNumber: 10,
				Severity:   "Critical",
				Comment:    "### Problem Title\n\nObservation:\nThis is bad.\n\n```go\n// code here\n```\n\n#### Recommendation\nFix it.",
			},
			contains: []string{
				"### 🛡️ Problem Title",
				"> [!CAUTION]",
				"This is bad.",
				"```go",
				"#### Recommendation",
				"Fix it.",
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
			name: "Windows path with backslash is handled",
			sug: core.Suggestion{
				FilePath: "C:\\path\\test.go", LineNumber: 5, Severity: "Low",
				Comment: "Fix this",
			},
			contains: []string{"### 🛡️ Code Review Finding"},
		},
		{
			name: "invalid line number returns empty string",
			sug: core.Suggestion{
				FilePath: "test.go", LineNumber: 0, Severity: "Medium",
				Comment: "Fix this",
			},
			contains: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatInlineComment(tt.sug)
			for _, c := range tt.contains {
				assert.Contains(t, got, c)
			}
			for _, e := range tt.excludes {
				assert.NotContains(t, got, e)
			}
		})
	}
}
