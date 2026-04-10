package stringsutil

import (
	"strings"
	"unicode/utf8"
)

// Truncate truncates a string to maxLen runes, adding a suffix if truncated.
// If maxLen is less than or equal to the string's rune count, returns the string
// unchanged. Safe for multi-byte UTF-8 input.
func Truncate(s string, maxLen int, suffix string) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	suffixRunes := []rune(suffix)
	if maxLen <= len(suffixRunes) {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-len(suffixRunes)]) + suffix
}

// TruncateLeft truncates from the left side, keeping the right portion.
// Safe for multi-byte UTF-8 input.
func TruncateLeft(s string, maxLen int, prefix string) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	prefixRunes := []rune(prefix)
	if maxLen <= len(prefixRunes) {
		return string(runes[len(runes)-maxLen:])
	}
	return string(prefixRunes) + string(runes[len(runes)-maxLen+len(prefixRunes):])
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
