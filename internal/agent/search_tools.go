package agent

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/mcp/tools"
)

const (
	grepDefaultLimit   = 50
	grepMaxOutputBytes = 50 * 1024 // 50 KB
	findDefaultLimit   = 200
)

// ── grep tool ─────────────────────────────────────────────────────────────────

// grepTool searches file contents with ripgrep (or grep as fallback).
// The binary is resolved once at construction time.
type grepTool struct {
	binary string // "rg" or "grep"
}

// newGrepTool constructs a grepTool, preferring ripgrep when available.
func newGrepTool() *grepTool {
	binary := "grep"
	if _, err := exec.LookPath("rg"); err == nil {
		binary = "rg"
	}
	return &grepTool{binary: binary}
}

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Description() string {
	return `Search for a pattern inside files in the workspace.
Uses ripgrep or grep. Returns matching lines in "file:line: content" format.
Path is relative to the workspace root.`
}

func (t *grepTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Search pattern (regular expression by default)",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory or file to search within the workspace (default: workspace root)",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Restrict search to files matching this glob, e.g. *.go or **/*_test.go",
			},
			"ignore_case": map[string]any{
				"type":        "boolean",
				"description": "Case-insensitive search (default: false)",
			},
			"context": map[string]any{
				"type":        "integer",
				"description": "Lines of context to show before and after each match (default: 0)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum output lines to return (default: %d)", grepDefaultLimit),
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *grepTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	root := tools.ProjectRootFromContext(ctx)

	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	relPath := "."
	if p, ok := args["path"].(string); ok && p != "" {
		relPath = p
	}

	// Validate path stays within the workspace root.
	if _, err := safeJoin(root, relPath); err != nil {
		return nil, err
	}

	limit := grepDefaultLimit
	if l := parseIntArg(args, "limit"); l > 0 {
		limit = l
	}

	ignoreCase, _ := args["ignore_case"].(bool)
	contextLines := parseIntArg(args, "context")
	glob, _ := args["glob"].(string)

	slog.Debug("grep tool",
		"pattern", pattern, "path", relPath, "glob", glob,
		"ignore_case", ignoreCase, "context", contextLines, "limit", limit,
		"binary", t.binary)

	cmdArgs := t.buildArgs(pattern, relPath, glob, ignoreCase, contextLines)
	cmd := exec.CommandContext(ctx, t.binary, cmdArgs...) //nolint:gosec // binary is "rg" or "grep", relPath is safeJoin-validated
	cmd.Dir = root                                        // paths in output are relative to workspace root

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = nil // exit code 1 from grep/rg means "no matches" — not an error

	_ = cmd.Run() // non-zero exit is expected when no matches found

	output := buf.String()

	// Apply line limit.
	limitReached := false
	outputLines := strings.Split(output, "\n")
	if len(outputLines) > limit {
		outputLines = outputLines[:limit]
		limitReached = true
	}
	output = strings.Join(outputLines, "\n")

	// Apply byte limit.
	truncated := false
	if len(output) > grepMaxOutputBytes {
		cut := strings.LastIndexByte(output[:grepMaxOutputBytes], '\n') + 1
		if cut <= 0 {
			cut = grepMaxOutputBytes
		}
		output = output[:cut] + "... [output truncated]"
		truncated = true
		limitReached = true
	}

	// Count non-blank, non-separator lines as the match count.
	count := 0
	for l := range strings.SplitSeq(output, "\n") {
		if l != "" && l != "--" {
			count++
		}
	}

	result := map[string]any{
		"output": strings.TrimRight(output, "\n"),
		"count":  count,
	}
	if truncated {
		result["truncated"] = true
	}
	if limitReached {
		result["limit_reached"] = true
		result["hint"] = fmt.Sprintf(
			"Results may be incomplete (limit=%d). Use a more specific pattern or increase the limit parameter.",
			limit,
		)
	}

	slog.Info("grep tool result",
		"pattern", pattern, "path", relPath, "count", count,
		"truncated", truncated, "limit_reached", limitReached)

	return result, nil
}

// buildArgs constructs the argument slice for rg or grep.
func (t *grepTool) buildArgs(pattern, relPath, glob string, ignoreCase bool, contextLines int) []string {
	if t.binary == "rg" {
		return buildRgArgs(pattern, relPath, glob, ignoreCase, contextLines)
	}
	return buildGrepArgs(pattern, relPath, glob, ignoreCase, contextLines)
}

func buildRgArgs(pattern, relPath, glob string, ignoreCase bool, contextLines int) []string {
	args := []string{"--no-heading", "--line-number"}
	if glob != "" {
		args = append(args, "--glob", glob)
	}
	if ignoreCase {
		args = append(args, "-i")
	}
	if contextLines > 0 {
		args = append(args, "-C", strconv.Itoa(contextLines))
	}
	return append(args, "--", pattern, relPath)
}

func buildGrepArgs(pattern, relPath, glob string, ignoreCase bool, contextLines int) []string {
	args := []string{"-rn"}
	if glob != "" {
		args = append(args, "--include="+glob)
	}
	if ignoreCase {
		args = append(args, "-i")
	}
	if contextLines > 0 {
		args = append(args, "-C", strconv.Itoa(contextLines))
	}
	return append(args, "--", pattern, relPath)
}

// ── find tool ─────────────────────────────────────────────────────────────────

// findTool lists workspace files matching a glob pattern.
// Implemented in pure Go — no external binary required.
type findTool struct{}

func (t *findTool) Name() string { return "find" }

func (t *findTool) Description() string {
	return `Find files in the workspace by name pattern.
Supports glob patterns including ** for multi-level matching (e.g. **/*_test.go).
Path is relative to the workspace root.
Skips .git and node_modules directories automatically.`
}

func (t *findTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern to match, e.g. *.go, **/*_test.go, internal/agent/*.go",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to search within the workspace (default: workspace root)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum number of files to return (default: %d)", findDefaultLimit),
			},
		},
		"required": []string{"pattern"},
	}
}

// skippedDirs contains directory names that are always excluded from find results.
var skippedDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
}

func (t *findTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	root := tools.ProjectRootFromContext(ctx)

	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	relPath := "."
	if p, ok := args["path"].(string); ok && p != "" {
		relPath = p
	}
	searchRoot, err := safeJoin(root, relPath)
	if err != nil {
		return nil, err
	}

	limit := findDefaultLimit
	if l := parseIntArg(args, "limit"); l > 0 {
		limit = l
	}

	slog.Debug("find tool", "pattern", pattern, "path", relPath, "limit", limit)

	var matches []string
	truncated := false

	walkFn := newFindWalkFn(root, searchRoot, pattern, limit, &matches, &truncated)
	if err := filepath.WalkDir(searchRoot, walkFn); err != nil {
		return nil, fmt.Errorf("find: %w", err)
	}

	result := map[string]any{
		"files": matches,
		"count": len(matches),
	}
	if truncated {
		result["truncated"] = true
		result["hint"] = fmt.Sprintf(
			"Results truncated at %d files. Narrow the search path or use a more specific pattern.",
			limit,
		)
	}
	slog.Info("find tool result",
		"pattern", pattern, "path", relPath, "count", len(matches), "truncated", truncated)

	return result, nil
}

// newFindWalkFn returns a filepath.WalkDir callback that collects files
// matching pattern, appending workspace-relative paths to *matches and setting
// *truncated when limit is reached.
func newFindWalkFn(root, searchRoot, pattern string, limit int, matches *[]string, truncated *bool) fs.WalkDirFunc {
	return func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(root, absPath)
		if relErr != nil {
			return nil //nolint:nilerr // paths derived from root; Rel only fails on unrooted inputs
		}
		rel = filepath.ToSlash(rel)

		relToSearch, relErr := filepath.Rel(searchRoot, absPath)
		if relErr != nil {
			return nil //nolint:nilerr // same guarantee as above
		}

		if !matchGlob(pattern, filepath.ToSlash(relToSearch)) {
			return nil
		}

		if len(*matches) >= limit {
			*truncated = true
			return fs.SkipAll
		}
		*matches = append(*matches, rel)
		return nil
	}
}

// matchGlob reports whether relPath matches the given glob pattern.
// Supports ** as a multi-segment wildcard and bare patterns (no /) as
// basename-only matches.
func matchGlob(pattern, relPath string) bool {
	pattern = filepath.ToSlash(pattern)
	relPath = filepath.ToSlash(relPath)

	if !strings.Contains(pattern, "**") {
		if !strings.Contains(pattern, "/") {
			// Basename-only: *.go, foo.go
			m, _ := path.Match(pattern, path.Base(relPath))
			return m
		}
		// Path-rooted: internal/agent/*.go
		m, _ := path.Match(pattern, relPath)
		return m
	}
	return matchDoublestar(pattern, relPath)
}

// matchDoublestar matches a pattern that contains at least one ** against s.
// ** matches zero or more path segments.
func matchDoublestar(pattern, s string) bool {
	prefix, after, _ := strings.Cut(pattern, "**")
	rest := strings.TrimPrefix(after, "/")

	// The prefix (everything before **) must match the beginning of s.
	if prefix != "" {
		if !strings.HasPrefix(s+"/", prefix) {
			return false
		}
		s = strings.TrimPrefix(s, strings.TrimSuffix(prefix, "/"))
		s = strings.TrimPrefix(s, "/")
	}

	// ** matches zero or more segments. Try each possible split.
	if rest == "" {
		return true // trailing ** matches everything
	}
	parts := strings.Split(s, "/")
	for i := 0; i <= len(parts); i++ {
		candidate := strings.Join(parts[i:], "/")
		if matchGlob(rest, candidate) {
			return true
		}
	}
	return false
}

// searchTools returns the workspace search tools (grep + find).
// Both are read-only and safe to register in the planner loop.
func searchTools() []mcp.Tool {
	return []mcp.Tool{
		newGrepTool(),
		&findTool{},
	}
}
