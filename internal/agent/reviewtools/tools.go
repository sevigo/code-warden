// Package reviewtools provides the read-only workspace tools used by review
// agents to investigate code without modifying it.
package reviewtools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	goframeagent "github.com/sevigo/goframe/agent"
)

// New constructs the complete read-only review toolset for a workspace.
func New(projectRoot string) []goframeagent.Tool {
	return []goframeagent.Tool{
		NewGrep(projectRoot),
		NewFind(projectRoot),
		NewReadFile(projectRoot),
		NewListDir(projectRoot),
	}
}

// NewReadFile returns a tool that reads a workspace file in bounded chunks.
func NewReadFile(projectRoot string) goframeagent.Tool {
	return &readFileTool{projectRoot: projectRoot}
}

// NewListDir returns a tool that lists a workspace directory.
func NewListDir(projectRoot string) goframeagent.Tool {
	return &listDirTool{projectRoot: projectRoot}
}

type readFileTool struct {
	projectRoot string
}

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

func (t *readFileTool) Execute(_ context.Context, args map[string]any) (any, error) {
	relPath, ok := args["path"].(string)
	if !ok || relPath == "" {
		return nil, fmt.Errorf("path is required")
	}
	abs, err := safeJoin(t.projectRoot, relPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read_file: %w", err)
	}

	allLines := strings.Split(string(data), "\n")
	totalLines := len(allLines)
	if totalLines > 0 && allLines[totalLines-1] == "" {
		totalLines--
	}

	offset := parseIntArg(args, "offset")
	if offset > 0 {
		offset--
	}
	if offset >= totalLines {
		return map[string]any{"content": "", "lines": 0}, nil
	}
	lines := allLines[offset:]

	limit := parseIntArg(args, "limit")
	truncated := limit > 0 && limit < len(lines)
	if truncated {
		lines = lines[:limit]
	}

	result := map[string]any{
		"content": strings.Join(lines, "\n"),
		"lines":   len(lines),
		"path":    relPath,
	}
	if truncated {
		nextOffset := offset + limit + 1
		result["total_lines"] = totalLines
		result["truncated"] = true
		result["hint"] = fmt.Sprintf(
			"File has %d lines total; output stopped at line %d. Use offset=%d to read the next chunk.",
			totalLines, offset+limit, nextOffset,
		)
	}
	return result, nil
}

type listDirTool struct {
	projectRoot string
}

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

func (t *listDirTool) Execute(_ context.Context, args map[string]any) (any, error) {
	relPath := "."
	if path, ok := args["path"].(string); ok && path != "" {
		relPath = path
	}

	abs, err := safeJoin(t.projectRoot, relPath)
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
	for _, item := range entries {
		kind := "file"
		if item.IsDir() {
			kind = "dir"
		}
		var size int64
		if !item.IsDir() {
			if info, infoErr := item.Info(); infoErr == nil {
				size = info.Size()
			}
		}
		result = append(result, entry{Name: item.Name(), Type: kind, Size: size})
	}
	return map[string]any{"path": relPath, "entries": result}, nil
}

func safeJoin(root, relPath string) (string, error) {
	abs := filepath.Clean(filepath.Join(root, relPath))
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes workspace root", relPath)
	}
	return abs, nil
}

func parseIntArg(args map[string]any, key string) int {
	value, ok := args[key]
	if !ok {
		return 0
	}
	switch number := value.(type) {
	case int:
		return number
	case float64:
		return int(number)
	default:
		return 0
	}
}
