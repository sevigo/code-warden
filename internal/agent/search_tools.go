package agent

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
	searchRoot, err := safeJoin(root, relPath)
	if err != nil {
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

	output := t.runGrep(ctx, root, searchRoot, relPath, pattern, glob, ignoreCase, contextLines)

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

// runGrep executes the search using the configured external binary (rg or
// grep) when available, and falls back to a pure-Go implementation otherwise.
// The fallback makes the tool portable on systems without ripgrep/grep on
// PATH (e.g. stock Windows hosts).
func (t *grepTool) runGrep(ctx context.Context, root, searchRoot, relPath, pattern, glob string, ignoreCase bool, contextLines int) string {
	// Resolve the binary fresh each call — LookPath is cheap and the PATH may
	// have changed since construction (mainly relevant in tests).
	binary := t.binary
	if _, err := exec.LookPath(binary); err != nil {
		binary = ""
	}

	if binary != "" {
		cmdArgs := t.buildArgsFor(binary, pattern, relPath, glob, ignoreCase, contextLines)
		cmd := exec.CommandContext(ctx, binary, cmdArgs...)
		cmd.Dir = root // paths in output are relative to workspace root

		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = nil // exit code 1 from grep/rg means "no matches" — not an error

		if err := cmd.Run(); err == nil {
			return buf.String()
		}
		// Non-zero exit doesn't necessarily mean "no matches" for rg/grep, but
		// the output buffer is what we care about. If Run failed for a reason
		// other than exit code 1 (e.g. binary not found mid-run), fall through
		// to the pure-Go implementation.
		if buf.Len() > 0 {
			return buf.String()
		}
	}

	return grepPureGo(root, searchRoot, pattern, glob, ignoreCase, contextLines)
}

// buildArgsFor dispatches to the binary-specific arg builder.
func (t *grepTool) buildArgsFor(binary, pattern, relPath, glob string, ignoreCase bool, contextLines int) []string {
	if binary == "rg" {
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

// grepPureGo is a portable reimplementation of `grep -rn <pattern> <path>`
// used as a fallback when neither rg nor grep is on PATH (e.g. stock Windows
// hosts). It walks searchRoot, applies the optional --include glob, and emits
// matching lines in the same "file:line: content" format the external tools
// produce. Context lines (--context / -C) are emitted as the external tools
// would: surrounding lines without a line-number prefix, separated from the
// next match group by "--".
func grepPureGo(root, searchRoot, pattern, glob string, ignoreCase bool, contextLines int) string {
	re := compileGrepRegex(pattern, ignoreCase)

	var out strings.Builder
	walkFn := newGrepWalkFn(root, searchRoot, glob, re, contextLines, &out)
	_ = filepath.WalkDir(searchRoot, walkFn)

	return out.String()
}

// compileGrepRegex builds the regexp used by the pure-Go grep fallback. On
// invalid regex syntax, it falls back to a literal substring search so the
// tool still returns something useful instead of an empty result.
func compileGrepRegex(pattern string, ignoreCase bool) *regexp.Regexp {
	prefix := ""
	if ignoreCase {
		prefix = "(?i)"
	}
	if re, err := regexp.Compile(prefix + pattern); err == nil {
		return re
	}
	return regexp.MustCompile(prefix + regexp.QuoteMeta(pattern))
}

// newGrepWalkFn returns a filepath.WalkDir callback that appends matching
// lines (with optional context) to out.
func newGrepWalkFn(root, searchRoot, glob string, re *regexp.Regexp, contextLines int, out *strings.Builder) fs.WalkDirFunc {
	return func(absPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
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
		return grepEmitFileMatches(root, searchRoot, absPath, glob, re, contextLines, out)
	}
}

// grepEmitFileMatches processes a single file: applies the glob filter, reads
// the file, finds matching lines, and writes them (with optional context) to
// out. Returns nil so WalkDir continues. All per-file errors (unreadable,
// binary, path resolution) are silently skipped — this mirrors grep's behavior
// of skipping files it can't read rather than aborting the whole search.
func grepEmitFileMatches(root, searchRoot, absPath, glob string, re *regexp.Regexp, contextLines int, out *strings.Builder) error {
	relToSearch, err := filepath.Rel(searchRoot, absPath)
	if err != nil {
		return nil //nolint:nilerr // skip files we can't resolve — mirrors grep
	}
	relToSearch = filepath.ToSlash(relToSearch)

	if glob != "" && !matchGlob(glob, relToSearch) {
		return nil
	}

	// Display path is relative to the workspace root, matching rg/grep
	// invoked with cmd.Dir=root.
	relToRoot, err := filepath.Rel(root, absPath)
	if err != nil {
		return nil //nolint:nilerr // skip files we can't resolve — mirrors grep
	}
	displayPath := filepath.ToSlash(relToRoot)

	content, err := readFileLines(absPath)
	if err != nil {
		return nil //nolint:nilerr // skip binary/unreadable files, like grep would
	}

	matches := grepFileLines(content, re)
	if len(matches) == 0 {
		return nil
	}

	writeMatchesWithContext(out, displayPath, content, matches, contextLines)
	return nil
}

// grepFileLines returns the 0-based line indices where the regexp matches.
// The ignoreCase flag is encoded into the regexp via the (?i) prefix at
// compile time, so no per-line branching is needed here.
func grepFileLines(lines []string, re *regexp.Regexp) []int {
	var matches []int
	for i, line := range lines {
		if re.MatchString(line) {
			matches = append(matches, i)
		}
	}
	return matches
}

// writeMatchesWithContext emits matching lines in "file:line: content" format
// and surrounding context lines (without the prefix) separated by "--" between
// groups, mirroring grep -C / rg -C output.
func writeMatchesWithContext(out *strings.Builder, displayPath string, lines []string, matches []int, contextLines int) {
	groups := groupAdjacentMatches(matches, contextLines)
	for gIdx, group := range groups {
		if gIdx > 0 {
			out.WriteString("--\n")
		}
		for _, ln := range group {
			if ln < 0 || ln >= len(lines) {
				continue
			}
			isMatch := false
			for _, m := range matches {
				if m == ln {
					isMatch = true
					break
				}
			}
			if isMatch {
				fmt.Fprintf(out, "%s:%d:%s\n", displayPath, ln+1, lines[ln])
			} else {
				fmt.Fprintf(out, "%s-%d-%s\n", displayPath, ln+1, lines[ln])
			}
		}
	}
}

// groupAdjacentMatches expands each match by contextLines on either side and
// merges overlapping expansions into contiguous groups.
func groupAdjacentMatches(matches []int, contextLines int) [][]int {
	if len(matches) == 0 {
		return nil
	}
	var groups [][]int
	current := expandMatch(matches[0], contextLines, 0, -1)
	for _, m := range matches[1:] {
		expanded := expandMatch(m, contextLines, 0, -1)
		if expanded[0] <= current[len(current)-1]+1 {
			// Merge: extend the current group.
			current = mergeGroups(current, expanded)
		} else {
			groups = append(groups, current)
			current = expanded
		}
	}
	groups = append(groups, current)
	return groups
}

func expandMatch(m, contextLines, lo, hi int) []int {
	start := m - contextLines
	if start < lo {
		start = lo
	}
	end := m + contextLines
	if hi >= 0 && end > hi {
		end = hi
	}
	if end < start {
		start = end
	}
	group := make([]int, 0, end-start+1)
	for i := start; i <= end; i++ {
		group = append(group, i)
	}
	return group
}

func mergeGroups(a, b []int) []int {
	seen := make(map[int]bool, len(a)+len(b))
	merged := make([]int, 0, len(a)+len(b))
	for _, i := range a {
		if !seen[i] {
			seen[i] = true
			merged = append(merged, i)
		}
	}
	for _, i := range b {
		if !seen[i] {
			seen[i] = true
			merged = append(merged, i)
		}
	}
	// Sort the merged slice.
	for i := 1; i < len(merged); i++ {
		for j := i; j > 0 && merged[j-1] > merged[j]; j-- {
			merged[j-1], merged[j] = merged[j], merged[j-1]
		}
	}
	return merged
}

// readFileLines reads a file and splits it into lines without a trailing empty
// element. Binary or unreadable files return an error.
func readFileLines(absPath string) ([]string, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	if isBinary(data) {
		return nil, fmt.Errorf("binary file")
	}
	// Normalise CRLF so the output doesn't include trailing \r on Windows.
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	// Drop a single trailing empty element from the final newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

// isBinary returns true if the data contains a NUL byte or a high proportion
// of non-text bytes in the first 512 bytes — a heuristic used by ripgrep and
// git to skip binary files.
func isBinary(data []byte) bool {
	const sampleSize = 512
	sample := data
	if len(sample) > sampleSize {
		sample = sample[:sampleSize]
	}
	nonText := 0
	for _, b := range sample {
		if b == 0 {
			return true
		}
		if b < 0x07 || (b > 0x0D && b < 0x20) {
			nonText++
		}
	}
	return len(sample) > 0 && nonText*100/len(sample) > 30
}
