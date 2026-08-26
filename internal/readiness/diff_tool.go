package readiness

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// diffTool lets the readiness agent re-fetch the diff and a structured
// changed-lines summary on demand, mirroring the generic review's get_diff tool.
type diffTool struct {
	diff     string
	fileLine map[string]map[int]struct{}
}

func newDiffTool(diff string, fileLine map[string]map[int]struct{}) *diffTool {
	return &diffTool{diff: diff, fileLine: fileLine}
}

func (t *diffTool) Name() string { return "get_diff" }

func (t *diffTool) Description() string {
	return `Re-fetch the PR diff and changed-line summary.

Call this FIRST to anchor your review on what actually changed, and again
whenever you lose track of the diff after investigating surrounding code.

Returns:
  - "diff": the full unified diff of the pull request
  - "changed_lines": per-file list of changed/new line numbers (the only valid
    line numbers you may cite in <line> tags)
  - "files": list of changed filenames`
}

func (t *diffTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"section": map[string]any{
				"type":        "string",
				"description": "Optional: return only one section - \"diff\", \"changed_lines\", or \"files\".",
				"enum":        []string{"diff", "changed_lines", "files"},
			},
		},
	}
}

func (t *diffTool) Execute(_ context.Context, args map[string]any) (any, error) {
	section, _ := args["section"].(string)
	switch section {
	case "diff":
		return map[string]any{"diff": t.diff}, nil
	case "changed_lines":
		return map[string]any{"changed_lines": t.changedLines()}, nil
	case "files":
		return map[string]any{"files": t.filenames()}, nil
	default:
		return map[string]any{
			"diff":          t.diff,
			"changed_lines": t.changedLines(),
			"files":         t.filenames(),
		}, nil
	}
}

func (t *diffTool) changedLines() string {
	var b strings.Builder
	for _, file := range sortedKeys(t.fileLine) {
		lines := t.fileLine[file]
		ns := make([]int, 0, len(lines))
		for ln := range lines {
			ns = append(ns, ln)
		}
		sort.Ints(ns)
		fmt.Fprintf(&b, "%s: ", file)
		for i, ln := range ns {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "%d", ln)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *diffTool) filenames() string {
	return strings.Join(sortedKeys(t.fileLine), "\n")
}

func sortedKeys(m map[string]map[int]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
