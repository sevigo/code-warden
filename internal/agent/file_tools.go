package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/mcp/tools"
)

// readFileTool reads a file from the agent workspace.
type readFileTool struct{}

func (t *readFileTool) Name() string { return "read_file" }

func (t *readFileTool) Description() string {
	return `Read the contents of a file in the workspace.
Path is relative to the workspace root.
Returns the file content as a string.`
}

func (t *readFileTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the file within the workspace",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Line number to start reading from (1-based, optional)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of lines to read (optional)",
			},
		},
		"required": []string{"path"},
	}
}

func (t *readFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	root := tools.ProjectRootFromContext(ctx)
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return nil, fmt.Errorf("path is required")
	}
	abs, err := safeJoin(root, relPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read_file: %w", err)
	}

	lines := strings.Split(string(data), "\n")

	offset := parseIntArg(args, "offset")
	if offset > 0 {
		offset-- // convert 1-based to 0-based
	}
	if offset >= len(lines) {
		return map[string]any{"content": "", "lines": 0}, nil
	}
	lines = lines[offset:]

	if limit := parseIntArg(args, "limit"); limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}

	return map[string]any{
		"content": strings.Join(lines, "\n"),
		"lines":   len(lines),
		"path":    relPath,
	}, nil
}

// writeFileTool writes (or creates) a file in the agent workspace.
type writeFileTool struct{}

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

	result := map[string]any{"ok": true, "path": relPath, "bytes": len(content)}
	return result, nil
}

// editFileTool replaces text in a file. Accepts either a single old_string/new_string
// pair (backwards-compatible) or an edits array for atomic multi-replacement.
// Mirrors Pi's edit tool semantics.
type editFileTool struct{}

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

	updated, usedFuzzy, err := applyMultiEdit(original, pairs)
	if err != nil {
		return nil, fmt.Errorf("edit_file: %w", err)
	}
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil { //nolint:gosec // G306: 0644 is intentional for source files
		return nil, fmt.Errorf("edit_file: write: %w", err)
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

// listDirTool lists the contents of a directory in the workspace.
type listDirTool struct{}

func (t *listDirTool) Name() string { return "list_dir" }

func (t *listDirTool) Description() string {
	return `List the contents of a directory in the workspace.
Path is relative to the workspace root. Defaults to the root if omitted.
Returns file names, types (file/dir), and sizes.`
}

func (t *listDirTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the directory (defaults to workspace root)",
			},
		},
	}
}

func (t *listDirTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	root := tools.ProjectRootFromContext(ctx)
	relPath := "."
	if p, ok := args["path"].(string); ok && p != "" {
		relPath = p
	}

	abs, err := safeJoin(root, relPath)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("list_dir: %w", err)
	}

	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size,omitempty"`
	}
	result := make([]entry, 0, len(entries))
	for _, e := range entries {
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		}
		var size int64
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
		}
		result = append(result, entry{Name: e.Name(), Type: kind, Size: size})
	}
	return map[string]any{"path": relPath, "entries": result}, nil
}

// fileTools returns all workspace file manipulation tools.
func fileTools() []mcp.Tool {
	return []mcp.Tool{
		&readFileTool{},
		&writeFileTool{},
		&editFileTool{},
		&listDirTool{},
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

// parseIntArg extracts an integer from args[key], accepting both int and float64
// (JSON numbers are typically unmarshalled as float64).
func parseIntArg(args map[string]any, key string) int {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
