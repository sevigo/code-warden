package review

import (
	"context"
	"fmt"
	"strings"

	goframeagent "github.com/sevigo/goframe/agent"
)

// getDiffTool lets the review agent re-fetch the PR diff and a structured
// changed-lines summary on demand. The diff is provided once as task context,
// but after several tool calls it scrolls out of the model's attention window.
// Offering get_diff as a tool lets the agent re-anchor itself on the actual
// changes instead of reviewing from memory (or wandering the whole repo).
type getDiffTool struct {
	diff          string
	changedLines  string
	filenameIndex string
}

// newGetDiffTool builds a get_diff tool from the review's task context pieces.
// changedLines is the pre-rendered "file -> changed line numbers" summary and
// filenameIndex is the list of changed filenames (used when the agent asks for
// a file-level overview).
func newGetDiffTool(diff, changedLines, filenameIndex string) *getDiffTool {
	return &getDiffTool{
		diff:          diff,
		changedLines:  changedLines,
		filenameIndex: filenameIndex,
	}
}

func (t *getDiffTool) Name() string { return "get_diff" }

func (t *getDiffTool) Description() string {
	return `Re-fetch the PR diff and changed-line summary.

Call this FIRST to anchor your review on what actually changed, and again
whenever you lose track of the diff after investigating surrounding code.

Returns:
  - "diff": the full unified diff of the pull request
  - "changed_lines": per-file list of changed/new line numbers (the only valid
    line numbers you may cite in <line> tags)
  - "files": list of changed filenames`
}

func (t *getDiffTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"section": map[string]any{
				"type":        "string",
				"description": "Optional: return only one section - \"diff\", \"changed_lines\", or \"files\". Omit to return all.",
				"enum":        []string{"diff", "changed_lines", "files"},
			},
		},
	}
}

func (t *getDiffTool) Execute(_ context.Context, args map[string]any) (any, error) {
	section, _ := args["section"].(string)
	switch section {
	case "diff":
		return map[string]any{"diff": t.diff}, nil
	case "changed_lines":
		return map[string]any{"changed_lines": t.changedLines}, nil
	case "files":
		return map[string]any{"files": t.filenameIndex}, nil
	default:
		return map[string]any{
			"diff":          t.diff,
			"changed_lines": t.changedLines,
			"files":         t.filenameIndex,
		}, nil
	}
}

// buildChangedLinesSummary renders a compact, per-file list of the new-side
// line numbers that are valid for inline findings. The agent uses this to pick
// accurate <line> values instead of guessing from @@ hunk headers.
func buildChangedLinesSummary(fileLines map[string]map[int]struct{}) string {
	if len(fileLines) == 0 {
		return ""
	}
	var b strings.Builder
	for file, lines := range fileLines {
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s: ", file)
		first := true
		for ln := range lines {
			if !first {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "%d", ln)
			first = false
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildFilenameIndex renders the list of changed filenames, one per line.
func buildFilenameIndex(fileLines map[string]map[int]struct{}) string {
	if len(fileLines) == 0 {
		return ""
	}
	var b strings.Builder
	for file := range fileLines {
		b.WriteString(file)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Compile-time interface check.
var _ goframeagent.Tool = (*getDiffTool)(nil)
