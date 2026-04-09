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
//
// Only lsp_diagnostics is exposed. Coordinate-based tools (lsp_definition,
// lsp_references, lsp_hover) require precise (line, col) positions that
// small-context LLMs like GLM-5.1 and MiniMax M2.7 rarely emit correctly,
// causing frequent tool errors without useful signal.
func Tools(mgr *Manager) []mcp.Tool {
	if mgr == nil || !mgr.Available() {
		return nil
	}
	return []mcp.Tool{
		&diagnosticsTool{mgr: mgr},
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

// --- helpers ---

// resolveWorkspacePath extracts "path" from args and resolves it to an absolute
// path within the workspace (obtained from context). It rejects paths that
// escape the workspace root using filepath.Rel rather than string prefix matching,
// which is safer on case-insensitive or symlink-heavy file systems.
func resolveWorkspacePath(ctx context.Context, args map[string]any) (string, error) {
	root := filepath.Clean(tools.ProjectRootFromContext(ctx))
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	abs := filepath.Clean(filepath.Join(root, relPath))
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes workspace root", relPath)
	}
	return abs, nil
}

// formatDiagnostics converts []Diagnostic to a human-readable agent result.
// ok is true unless at least one diagnostic has SeverityError — warnings,
// hints, and info messages are surfaced but do not mark the file as broken.
func formatDiagnostics(diags []Diagnostic) map[string]any {
	if len(diags) == 0 {
		return map[string]any{"ok": true, "count": 0, "diagnostics": []any{}}
	}
	items := make([]map[string]any, 0, len(diags))
	hasErrors := false
	for _, d := range diags {
		items = append(items, map[string]any{
			"severity": d.Severity.String(),
			"line":     d.Range.Start.Line + 1, // convert to 1-based for readability
			"column":   d.Range.Start.Character + 1,
			"message":  d.Message,
			"source":   d.Source,
		})
		if d.Severity == SeverityError {
			hasErrors = true
		}
	}
	return map[string]any{
		"ok":          !hasErrors,
		"count":       len(diags),
		"diagnostics": items,
	}
}
