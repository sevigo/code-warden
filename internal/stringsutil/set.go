package stringsutil

import (
	"strings"
)

// ContainsAny reports whether s contains any of the substrings in needles.
// Returns false if needles is empty.
func ContainsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// JoinTruncated joins the first n elements of parts with sep, truncating the
// result to maxLen runes. If n is greater than len(parts), all elements are
// joined. If maxLen is 0, the result is returned untruncated.
func JoinTruncated(parts []string, n int, sep string, maxLen int) string {
	if n > len(parts) {
		n = len(parts)
	}
	joined := strings.Join(parts[:n], sep)
	if maxLen == 0 {
		return joined
	}
	return Truncate(joined, maxLen, "...")
}

// Dedupe returns a copy of items with duplicate elements removed, preserving
// the order of first occurrence.
func Dedupe(items []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

// MaskEmail hides the local part of an email address, showing only the first
// character followed by asterisks. For example, "alice@example.com" becomes
// "a***@example.com". If the address contains no "@", it returns the input
// unchanged.
func MaskEmail(email string) string {
	at := strings.Index(email, "@")
	if at < 0 {
		return email
	}
	local := email[:at]
	domain := email[at:]
	if len(local) <= 1 {
		return local + domain
	}
	return string(local[0]) + strings.Repeat("*", len(local)-1) + domain
}
