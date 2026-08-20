package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/sevigo/code-warden/internal/agent/reviewtools"
	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/mcp/tools"
)

// writeFileTool writes (or creates) a file in the agent workspace.
type writeFileTool struct {
	Formatter *Formatter
}

func (t *writeFileTool) Name() string { return "write_file" }

func (t *writeFileTool) Description() string {
	return `Write content to a file in the workspace, creating it (and any parent
directories) if it does not exist. Overwrites the file completely.
Path is relative to the workspace root.`
}

func (t *writeFileTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the file within the workspace",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The full file content to write",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *writeFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	root := tools.ProjectRootFromContext(ctx)
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return nil, fmt.Errorf("path is required")
	}
	content, ok := args["content"].(string)
	if !ok {
		return nil, fmt.Errorf("content is required")
	}

	abs, err := safeJoin(root, relPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, fmt.Errorf("write_file: create parent dirs: %w", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil { //nolint:gosec // G306: 0644 is intentional for source files
		return nil, fmt.Errorf("write_file: %w", err)
	}

	// Auto-format the written file if a formatter is available, then re-read
	// so the diff the LLM sees matches what's on disk.
	written := content
	if t.Formatter != nil {
		t.Formatter.Format(ctx, root, abs)
		if formatted, readErr := os.ReadFile(abs); readErr == nil {
			written = string(formatted)
		}
	}

	result := map[string]any{"ok": true, "path": relPath, "bytes": len(written)}
	return result, nil
}

// editFileTool replaces text in a file. Accepts either a single old_string/new_string
// pair (backwards-compatible) or an edits array for atomic multi-replacement.
type editFileTool struct {
	Formatter *Formatter
}

func (t *editFileTool) Name() string { return "edit_file" }

func (t *editFileTool) Description() string {
	return `Replace text in a file using exact-then-fuzzy matching.

Two calling conventions are supported:

1. Single replacement (backwards-compatible):
   {"path": "...", "old_string": "...", "new_string": "..."}

2. Multiple atomic replacements:
   {"path": "...", "edits": [{"old_string": "...", "new_string": "..."}, ...]}

Each old_string must appear exactly once; the edit is rejected if it is
not found or is ambiguous. All replacements in edits[] are applied atomically
in a single pass (no partial writes). Use write_file to replace the entire file.
Path is relative to the workspace root.

Returns ok, path, an optional fuzzy_match flag, and a unified diff of the changes.`
}

func (t *editFileTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the file within the workspace",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Single replacement: the exact string to replace (must appear exactly once)",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "Single replacement: the string to replace it with",
			},
			"edits": map[string]any{
				"type":        "array",
				"description": "Multiple atomic replacements applied in one pass",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old_string": map[string]any{"type": "string"},
						"new_string": map[string]any{"type": "string"},
					},
					"required": []string{"old_string", "new_string"},
				},
			},
		},
		"required": []string{"path"},
	}
}

func (t *editFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	root := tools.ProjectRootFromContext(ctx)
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return nil, fmt.Errorf("path is required")
	}

	pairs, err := parseEditPairs(args)
	if err != nil {
		return nil, err
	}

	abs, err := safeJoin(root, relPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("edit_file: read: %w", err)
	}
	original := string(data)

	// Detect file-level properties before matching so we can restore them
	// after the edit. BOM and CRLF would otherwise prevent matching
	// because the LLM's old_string will always use LF and no BOM.
	working, hasBOM := stripBOM(original)
	lineEnding := detectLineEnding(working)
	working = normalizeLineEndings(working)

	// Normalize edit pairs to LF so they match the normalized content.
	normalizedPairs := make([]editPair, len(pairs))
	for i, p := range pairs {
		normalizedPairs[i] = editPair{
			OldStr: normalizeLineEndings(p.OldStr),
			NewStr: normalizeLineEndings(p.NewStr),
		}
	}

	updated, usedFuzzy, err := applyMultiEdit(working, normalizedPairs)
	if err != nil {
		var pe *partialEditError
		if errors.As(err, &pe) {
			return handlePartialEdit(editContext{
				abs: abs, relPath: relPath, original: original, working: updated,
				lineEnding: lineEnding, hasBOM: hasBOM, usedFuzzy: usedFuzzy,
				formatter: t.Formatter, workspaceRoot: root, ctx: ctx,
			}, pe, err)
		}
		return nil, fmt.Errorf("edit_file: %w", err)
	}

	// Restore the file's original line endings and BOM before writing.
	updated = restoreLineEndings(updated, lineEnding)
	updated = prependBOM(updated, hasBOM)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil { //nolint:gosec // G306: 0644 is intentional for source files
		return nil, fmt.Errorf("edit_file: write: %w", err)
	}

	// Auto-format the written file and re-read so the diff is accurate.
	if t.Formatter != nil {
		t.Formatter.Format(ctx, root, abs)
		if formatted, readErr := os.ReadFile(abs); readErr == nil {
			updated = string(formatted)
		}
	}

	result := map[string]any{"ok": true, "path": relPath}
	if usedFuzzy {
		result["fuzzy_match"] = true
	}

	// Generate a unified diff so the LLM can verify its changes without a
	// follow-up read_file call. Truncated to avoid flooding the context.
	diffStr := buildUnifiedDiff(original, updated, relPath)
	if diffStr != "" {
		result["diff"] = diffStr
	}

	return result, nil
}

// diffMaxBytes is the maximum number of bytes returned in the edit_file diff
// field. Large rewrites are truncated with a marker.
const diffMaxBytes = 4000

// parseEditPairs resolves the edit specification from tool args.
// Accepts two calling conventions:
//   - edits: [{old_string, new_string}, ...]  (multi-edit array)
//   - old_string / new_string flat keys       (single replacement, backwards-compat)
func parseEditPairs(args map[string]any) ([]editPair, error) {
	if rawEdits, ok := args["edits"]; ok {
		return parseEditsArray(rawEdits)
	}
	oldStr, ok := args["old_string"].(string)
	if !ok {
		return nil, fmt.Errorf("old_string is required (or provide edits array)")
	}
	newStr, _ := args["new_string"].(string)
	return []editPair{{OldStr: oldStr, NewStr: newStr}}, nil
}

// parseEditsArray decodes the multi-edit array format.
func parseEditsArray(rawEdits any) ([]editPair, error) {
	list, ok := rawEdits.([]any)
	if !ok {
		return nil, fmt.Errorf("edits must be an array")
	}
	pairs := make([]editPair, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edits[%d] must be an object", i)
		}
		oldStr, _ := m["old_string"].(string)
		newStr, _ := m["new_string"].(string)
		if oldStr == "" {
			return nil, fmt.Errorf("edits[%d].old_string is required", i)
		}
		pairs = append(pairs, editPair{OldStr: oldStr, NewStr: newStr})
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("edits array is empty")
	}
	return pairs, nil
}

// buildUnifiedDiff returns a unified diff string between original and updated,
// or an empty string if the diff cannot be produced.
// Truncation is done at a newline boundary to avoid splitting mid-line (and
// to sidestep any multi-byte UTF-8 boundary issues at the byte limit).
func buildUnifiedDiff(original, updated, path string) string {
	ud := difflib.UnifiedDiff{
		A:        difflib.SplitLines(original),
		B:        difflib.SplitLines(updated),
		FromFile: "a/" + path,
		ToFile:   "b/" + path,
		Context:  3,
	}
	s, err := difflib.GetUnifiedDiffString(ud)
	if err != nil || s == "" {
		return ""
	}
	if len(s) > diffMaxBytes {
		// Truncate at the last newline before the byte limit.
		cut := strings.LastIndexByte(s[:diffMaxBytes], '\n') + 1
		if cut <= 0 {
			cut = diffMaxBytes
		}
		return s[:cut] + "... [diff truncated]"
	}
	return s
}

// fileTools returns all workspace file manipulation tools.
// The formatter is used to auto-format Go files after write/edit; pass nil to disable.
func fileTools(formatter *Formatter, projectRoot string) []mcp.Tool {
	return []mcp.Tool{
		reviewtools.NewReadFile(projectRoot),
		&writeFileTool{Formatter: formatter},
		&editFileTool{Formatter: formatter},
		reviewtools.NewListDir(projectRoot),
	}
}

// safeJoin joins root and relPath, returning an error if the result escapes root.
func safeJoin(root, relPath string) (string, error) {
	abs := filepath.Clean(filepath.Join(root, relPath))
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes workspace root", relPath)
	}
	return abs, nil
}

// editContext holds file metadata needed to restore line endings and BOM
// after a partial edit, and to generate a diff for the LLM.
type editContext struct {
	abs, relPath, original, working, lineEnding string
	hasBOM, usedFuzzy                           bool
	formatter                                   *Formatter
	workspaceRoot                               string
	ctx                                         context.Context
}

func handlePartialEdit(ctx editContext, pe *partialEditError, origErr error) (map[string]any, error) {
	appliedCount := pe.TotalEdits - len(pe.FailedIndices)
	result := map[string]any{
		"ok":            false,
		"path":          ctx.relPath,
		"partial":       true,
		"applied_edits": appliedCount,
		"failed_edits":  pe.FailedIndices,
		"total_edits":   pe.TotalEdits,
		"error":         origErr.Error(),
	}
	if ctx.usedFuzzy {
		result["fuzzy_match"] = true
	}

	// Only write to disk if at least one edit succeeded.
	if appliedCount == 0 {
		return result, nil
	}

	partialResult := restoreLineEndings(ctx.working, ctx.lineEnding)
	partialResult = prependBOM(partialResult, ctx.hasBOM)
	if writeErr := os.WriteFile(ctx.abs, []byte(partialResult), 0o644); writeErr != nil { //nolint:gosec // G306: 0644 is intentional for source files
		return nil, fmt.Errorf("edit_file: partial edit write failed: %w", writeErr)
	}

	// Auto-format the partially-edited file.
	partialResult = formatAndReadback(ctx.ctx, ctx.formatter, ctx.workspaceRoot, ctx.abs, partialResult)

	diffStr := buildUnifiedDiff(ctx.original, partialResult, ctx.relPath)
	if diffStr != "" {
		result["diff"] = diffStr
	}

	return result, nil
}

// formatAndReadback runs the auto-formatter on the given file and re-reads it.
// Returns the (possibly formatted) content, or the original content if formatting
// didn't change the file or the formatter is nil.
func formatAndReadback(ctx context.Context, formatter *Formatter, workspaceRoot, absPath, current string) string {
	if formatter == nil {
		return current
	}
	formatter.Format(ctx, workspaceRoot, absPath)
	if formatted, readErr := os.ReadFile(absPath); readErr == nil {
		return string(formatted)
	}
	return current
}
