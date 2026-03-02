package agent

import (
	"crypto/rand"
	"fmt"
)

func generateSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("agent-%x", b)
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
