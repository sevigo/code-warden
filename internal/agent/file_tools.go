package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sevigo/code-warden/internal/agent/lsp"
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
	}, nil
}

// writeFileTool writes (or creates) a file in the agent workspace.
// When lsp is non-nil it notifies the language server of the change and
// appends any compiler diagnostics to the tool result — so the agent sees
// compile errors in the same turn it wrote the file.
type writeFileTool struct {
	lsp *lsp.Manager // may be nil
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

	result := map[string]any{"ok": true, "path": relPath, "bytes": len(content)}
	appendLSPDiagnostics(ctx, t.lsp, abs, content, result)
	return result, nil
}

// editFileTool replaces the first occurrence of old_string with new_string.
// Mirrors Claude Code's Edit tool semantics. When lsp is non-nil it appends
// compiler diagnostics to the result after the edit.
type editFileTool struct {
	lsp *lsp.Manager // may be nil
}

func (t *editFileTool) Name() string { return "edit_file" }

func (t *editFileTool) Description() string {
	return `Replace the first exact occurrence of old_string with new_string in a file.
The match must be unique — if old_string appears more than once the edit is
rejected. Use write_file to replace the entire file instead.
Path is relative to the workspace root.`
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
				"description": "The exact string to replace (must appear exactly once in the file)",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The string to replace it with",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

func (t *editFileTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	root := tools.ProjectRootFromContext(ctx)
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return nil, fmt.Errorf("path is required")
	}
	oldStr, ok := args["old_string"].(string)
	if !ok {
		return nil, fmt.Errorf("old_string is required")
	}
	newStr, ok := args["new_string"].(string)
	if !ok {
		return nil, fmt.Errorf("new_string is required")
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

	count := strings.Count(original, oldStr)
	if count == 0 {
		return nil, fmt.Errorf("edit_file: old_string not found in %s", relPath)
	}
	if count > 1 {
		return nil, fmt.Errorf("edit_file: old_string appears %d times in %s; provide more context to make it unique", count, relPath)
	}

	updated := strings.Replace(original, oldStr, newStr, 1)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil { //nolint:gosec // G306: 0644 is intentional for source files
		return nil, fmt.Errorf("edit_file: write: %w", err)
	}

	result := map[string]any{"ok": true, "path": relPath}
	appendLSPDiagnostics(ctx, t.lsp, abs, updated, result)
	return result, nil
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
// Pass a non-nil lsp.Manager to enable automatic diagnostic feedback after
// write_file and edit_file calls. Pass nil to disable LSP integration.
func fileTools(mgr *lsp.Manager) []mcp.Tool {
	return []mcp.Tool{
		&readFileTool{},
		&writeFileTool{lsp: mgr},
		&editFileTool{lsp: mgr},
		&listDirTool{},
	}
}

// appendLSPDiagnostics notifies the language server of a file change and, if
// any diagnostics are returned, adds them to result under the "diagnostics" key.
// It is a no-op when mgr is nil or when the file extension is not handled.
func appendLSPDiagnostics(ctx context.Context, mgr *lsp.Manager, absPath, content string, result map[string]any) {
	if mgr == nil {
		return
	}
	diags, err := mgr.NotifyChange(ctx, absPath, content)
	if err != nil || len(diags) == 0 {
		return
	}
	items := make([]map[string]any, 0, len(diags))
	hasErrors := false
	for _, d := range diags {
		items = append(items, map[string]any{
			"severity": d.Severity.String(),
			"line":     d.Range.Start.Line + 1,
			"column":   d.Range.Start.Character + 1,
			"message":  d.Message,
		})
		if d.Severity == lsp.SeverityError {
			hasErrors = true
		}
	}
	result["diagnostics"] = items
	if hasErrors {
		result["ok"] = false
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
