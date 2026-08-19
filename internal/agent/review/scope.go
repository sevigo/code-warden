package review

import (
	"strings"

	internalgithub "github.com/sevigo/code-warden/internal/github"
)

// scopeAngles filters the default angles to those likely relevant for the
// changed files in the PR. This prevents wasted tokens on angles that have no
// applicable findings (e.g. a security angle reviewing a README change, or a
// performance angle reviewing a doc-only PR).
//
// The heuristic is conservative: when in doubt, keep the angle. It only drops
// an angle when none of the changed files match its domain.
func scopeAngles(angles []Angle, changedFiles []internalgithub.ChangedFile) []Angle {
	if len(changedFiles) == 0 {
		return angles
	}
	var out []Angle
	for _, a := range angles {
		if angleRelevant(a, changedFiles) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return angles
	}
	return out
}

// angleRelevant reports whether at least one changed file could plausibly
// contain findings for the given angle.
func angleRelevant(a Angle, changedFiles []internalgithub.ChangedFile) bool {
	switch a.Name {
	case "security":
		for _, cf := range changedFiles {
			if isSecurityRelevant(cf.Filename, cf.Patch) {
				return true
			}
		}
		return false
	case "performance":
		for _, cf := range changedFiles {
			if isPerformanceRelevant(cf.Filename, cf.Patch) {
				return true
			}
		}
		return false
	case "bug", "conventions":
		// Bug and conventions angles are relevant for any code change.
		for _, cf := range changedFiles {
			if isCodeFile(cf.Filename) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// isCodeFile reports whether a filename looks like a source code file (not
// docs/config only).
func isCodeFile(filename string) bool {
	for _, ext := range []string{
		".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".java", ".kt",
		".rs", ".c", ".h", ".cpp", ".cc", ".hpp", ".rb", ".php", ".cs",
		".swift", ".m", ".scala", ".clj", ".ex", ".exs", ".erl", ".fs",
		".fsx", ".dart", ".lua", ".pl", ".r", ".jl", ".vue", ".svelte",
	} {
		if strings.HasSuffix(filename, ext) {
			return true
		}
	}
	return false
}

// isSecurityRelevant checks whether a file or its patch touches security-
// sensitive areas (auth, crypto, SQL, user input handling, etc.).
func isSecurityRelevant(filename string, patch string) bool {
	if !isCodeFile(filename) {
		return false
	}
	lowerPatch := strings.ToLower(patch)
	for _, kw := range []string{
		"auth", "token", "password", "secret", "crypto", "hash",
		"sql", "query", "exec", "command", "shell", "input", "param",
		"request", "header", "cookie", "session", "jwt", "permission",
		"admin", "privilege", "decrypt", "encrypt", "marshal", "unmarshal",
		"deserialize", "upload", "download", "filepath", "path.join",
	} {
		if strings.Contains(lowerPatch, kw) {
			return true
		}
	}
	return false
}

// isPerformanceRelevant checks whether a file or its patch touches performance-
// sensitive areas (loops, allocations, DB queries, caches, etc.).
func isPerformanceRelevant(filename string, patch string) bool {
	if !isCodeFile(filename) {
		return false
	}
	lowerPatch := strings.ToLower(patch)
	for _, kw := range []string{
		"for ", "range ", "loop", "select", "goroutine", "go func",
		"query", "sql", "db.", "cache", "map[", "append(",
		"alloc", "buffer", "stream", "channel", "mutex", "lock",
		"concurren", "parallel", "batch", "bulk", "iter",
	} {
		if strings.Contains(lowerPatch, kw) {
			return true
		}
	}
	return false
}
