package lsp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/mcp/tools"
)

// Tools returns the agent-facing LSP tool implementations for the given Manager.
// These tools are registered in the agent loop alongside the MCP tools and
// implement the mcp.Tool interface so they can be wrapped by contextInjectingTool.
// If mgr is nil or has no running servers, a nil slice is returned.
func Tools(mgr *Manager) []mcp.Tool {
	if mgr == nil || !mgr.Available() {
		return nil
	}
	return []mcp.Tool{
		&diagnosticsTool{mgr: mgr},
		&definitionTool{mgr: mgr},
		&referencesTool{mgr: mgr},
		&hoverTool{mgr: mgr},
	}
}

// --- lsp_diagnostics ---

type diagnosticsTool struct{ mgr *Manager }

func (t *diagnosticsTool) Name() string { return "lsp_diagnostics" }
func (t *diagnosticsTool) Description() string {
	return `Get compiler errors and warnings for a source file using the language server.
Use this after editing a file to verify there are no compile errors.
Path is relative to the workspace root.`
}
func (t *diagnosticsTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path to the source file",
			},
		},
		"required": []string{"path"},
	}
}
func (t *diagnosticsTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	absPath, err := resolveWorkspacePath(ctx, args)
	if err != nil {
		return nil, err
	}
	diags, err := t.mgr.Diagnostics(ctx, absPath)
	if err != nil {
		return nil, fmt.Errorf("lsp_diagnostics: %w", err)
	}
	return formatDiagnostics(diags), nil
}

// --- lsp_definition ---

type definitionTool struct{ mgr *Manager }

func (t *definitionTool) Name() string { return "lsp_definition" }
func (t *definitionTool) Description() string {
	return `Go to the definition of a symbol at a given position in a source file.
Returns the file path and line number where the symbol is defined.
Useful for navigating to function or type implementations.
Path is relative to the workspace root. Line and column are 0-based.`
}
func (t *definitionTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "Relative path to the source file"},
			"line":   map[string]any{"type": "integer", "description": "0-based line number"},
			"column": map[string]any{"type": "integer", "description": "0-based character offset"},
		},
		"required": []string{"path", "line", "column"},
	}
}
func (t *definitionTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	absPath, err := resolveWorkspacePath(ctx, args)
	if err != nil {
		return nil, err
	}
	line := parseIntArg(args, "line")
	col := parseIntArg(args, "column")

	locs, err := t.mgr.Definition(ctx, absPath, line, col)
	if err != nil {
		return nil, fmt.Errorf("lsp_definition: %w", err)
	}
	return formatLocations(t.mgr.workspace, locs), nil
}

// --- lsp_references ---

type referencesTool struct{ mgr *Manager }

func (t *referencesTool) Name() string { return "lsp_references" }
func (t *referencesTool) Description() string {
	return `Find all usages of the symbol at a given position in a source file.
Returns a list of file paths and line numbers where the symbol is referenced.
Use before renaming or modifying a function/type to understand impact.
Path is relative to the workspace root. Line and column are 0-based.`
}
func (t *referencesTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "Relative path to the source file"},
			"line":   map[string]any{"type": "integer", "description": "0-based line number"},
			"column": map[string]any{"type": "integer", "description": "0-based character offset"},
		},
		"required": []string{"path", "line", "column"},
	}
}
func (t *referencesTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	absPath, err := resolveWorkspacePath(ctx, args)
	if err != nil {
		return nil, err
	}
	line := parseIntArg(args, "line")
	col := parseIntArg(args, "column")

	locs, err := t.mgr.References(ctx, absPath, line, col)
	if err != nil {
		return nil, fmt.Errorf("lsp_references: %w", err)
	}
	return formatLocations(t.mgr.workspace, locs), nil
}

// --- lsp_hover ---

type hoverTool struct{ mgr *Manager }

func (t *hoverTool) Name() string { return "lsp_hover" }
func (t *hoverTool) Description() string {
	return `Get type information and documentation for the symbol at a given position.
Returns the type signature and any doc comment.
Useful when calling an unfamiliar function to understand its parameters and return type.
Path is relative to the workspace root. Line and column are 0-based.`
}
func (t *hoverTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "Relative path to the source file"},
			"line":   map[string]any{"type": "integer", "description": "0-based line number"},
			"column": map[string]any{"type": "integer", "description": "0-based character offset"},
		},
		"required": []string{"path", "line", "column"},
	}
}
func (t *hoverTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	absPath, err := resolveWorkspacePath(ctx, args)
	if err != nil {
		return nil, err
	}
	line := parseIntArg(args, "line")
	col := parseIntArg(args, "column")

	text, err := t.mgr.Hover(ctx, absPath, line, col)
	if err != nil {
		return nil, fmt.Errorf("lsp_hover: %w", err)
	}
	if text == "" {
		return map[string]any{"info": "no hover information available"}, nil
	}
	return map[string]any{"info": text}, nil
}

// --- helpers ---

// resolveWorkspacePath extracts "path" from args and resolves it to an absolute
// path within the workspace (obtained from context).
func resolveWorkspacePath(ctx context.Context, args map[string]any) (string, error) {
	root := tools.ProjectRootFromContext(ctx)
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	abs := filepath.Clean(filepath.Join(root, relPath))
	if !strings.HasPrefix(abs, root) {
		return "", fmt.Errorf("path %q escapes workspace root", relPath)
	}
	return abs, nil
}

// formatDiagnostics converts []Diagnostic to a human-readable agent result.
func formatDiagnostics(diags []Diagnostic) map[string]any {
	if len(diags) == 0 {
		return map[string]any{"ok": true, "count": 0, "diagnostics": []any{}}
	}
	items := make([]map[string]any, 0, len(diags))
	for _, d := range diags {
		items = append(items, map[string]any{
			"severity": d.Severity.String(),
			"line":     d.Range.Start.Line + 1, // convert to 1-based for readability
			"column":   d.Range.Start.Character + 1,
			"message":  d.Message,
			"source":   d.Source,
		})
	}
	return map[string]any{
		"ok":          false,
		"count":       len(diags),
		"diagnostics": items,
	}
}

// formatLocations converts []Location to relative paths for the agent.
func formatLocations(workspace string, locs []Location) map[string]any {
	results := make([]map[string]any, 0, len(locs))
	for _, loc := range locs {
		absPath := uriToPath(loc.URI)
		relPath, err := filepath.Rel(workspace, absPath)
		if err != nil {
			relPath = absPath
		}
		results = append(results, map[string]any{
			"path":   relPath,
			"line":   loc.Range.Start.Line + 1,   // 1-based
			"column": loc.Range.Start.Character + 1,
		})
	}
	return map[string]any{"locations": results, "count": len(results)}
}

// parseIntArg extracts an integer argument from the args map.
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
