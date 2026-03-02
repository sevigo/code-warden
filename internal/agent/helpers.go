package agent

import (
	"crypto/rand"
	"fmt"
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

// truncateString truncates a string to maxLen characters.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// truncateTail returns the last maxLen characters of a string.
func truncateTail(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return "... [truncated] ...\n" + s[len(s)-maxLen:]
}
