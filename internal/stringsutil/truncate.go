package stringsutil

import "strings"

// Truncate truncates a string to maxLen characters, adding a suffix if truncated.
// If maxLen is less than or equal to the string length, returns the string unchanged.
func Truncate(s string, maxLen int, suffix string) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= len(suffix) {
		return s[:maxLen]
	}
	return s[:maxLen-len(suffix)] + suffix
}

// TruncateLeft truncates from the left side, keeping the right portion.
func TruncateLeft(s string, maxLen int, prefix string) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= len(prefix) {
		return s[len(s)-maxLen:]
	}
	return prefix + s[len(s)-maxLen+len(prefix):]
}

// TruncateSHA truncates a git SHA to a standard short form (7 characters).
func TruncateSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Indent adds indentation to each line of a string.
func Indent(s, indent string) string {
	if s == "" {
		return s
	}
	return indent + strings.ReplaceAll(s, "\n", "\n"+indent)
}
