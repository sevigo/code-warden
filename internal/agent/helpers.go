package agent

import (
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sevigo/code-warden/internal/stringsutil"
)

// matrixAgentNames is a pool of Matrix-universe character and concept names
// used to generate memorable session identifiers.
var matrixAgentNames = []string{
	// Agents & programs
	"smith", "jones", "brown", "johnson", "thompson",
	"oracle", "architect", "keymaker", "merovingian", "persephone",
	"seraph", "rama-kandra", "kamala", "sati",
	"trainman", "exile", "twins",

	// Zion crew
	"neo", "morpheus", "trinity", "tank", "dozer",
	"apoc", "switch", "cypher", "mouse",
	"niobe", "ghost", "lock", "link",
	"zee", "kid", "roland",

	// Ships
	"nebuchadnezzar", "logos", "mjolnir", "hammer",
	"caduceus", "osiris", "icarus",

	// Concepts
	"redpill", "bluepill", "construct", "hardline",
	"residual", "simulation", "anomaly", "prophecy",
	"zion", "matrix", "source", "core",
}

// generateSessionID returns a unique agent session ID using a Matrix-themed name
// combined with a short random suffix to avoid collisions.
func generateSessionID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := matrixAgentNames[int(b[0])%len(matrixAgentNames)]
	return fmt.Sprintf("%s-%x", name, b[1:])
}

// sanitizeSessionID validates and sanitizes a session ID to prevent path traversal.
// Returns the session ID if valid, or an error if it contains dangerous characters.
func sanitizeSessionID(id string) (string, error) {
	// Session IDs should only contain alphanumeric characters, hyphens, and underscores
	for _, c := range id {
		if !isSafeChar(c) {
			return "", fmt.Errorf("invalid session ID: contains unsafe character %q", c)
		}
	}

	// Prevent path traversal
	clean := filepath.Base(id)
	if clean != id {
		return "", fmt.Errorf("invalid session ID: potential path traversal")
	}

	// Prevent hidden files (starting with .)
	if strings.HasPrefix(id, ".") {
		return "", fmt.Errorf("invalid session ID: cannot start with dot")
	}

	return id, nil
}

// isSafeChar returns true if the character is safe for session IDs.
func isSafeChar(c rune) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_'
}

// truncateString truncates a string to maxLen characters.
func truncateString(s string, maxLen int) string {
	return stringsutil.Truncate(s, maxLen, "...")
}

// truncateTail returns the last maxLen characters of a string.
func truncateTail(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return "... [truncated] ...\n" + s[len(s)-maxLen:]
}
