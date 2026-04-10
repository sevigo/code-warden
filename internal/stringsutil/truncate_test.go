package stringsutil

import (
	"testing"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		suffix   string
		expected string
	}{
		{"short string unchanged", "hello", 10, "...", "hello"},
		{"exact length unchanged", "hello", 5, "...", "hello"},
		{"ASCII truncation", "hello world", 8, "...", "hello..."},
		{"empty string", "", 10, "...", ""},
		{"suffix longer than maxLen", "hello world", 2, "...", "he"},
		{"multi-byte UTF-8 emoji in middle", "hello 🌍 world", 9, "...", "hello ..."},
		{"multi-byte CJK truncation", "你好世界测试", 4, "...", "你..."},
		{"emoji at truncation boundary", "ab🌍cd", 3, "...", "ab🌍"},
		{"empty suffix", "hello world", 5, "", "hello"},
		{"multi-byte suffix rune count", "hello world", 6, "…", "hello…"}, // "…" is 3 bytes but 1 rune
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Truncate(tt.input, tt.maxLen, tt.suffix)
			if result != tt.expected {
				t.Errorf("Truncate(%q, %d, %q) = %q, want %q", tt.input, tt.maxLen, tt.suffix, result, tt.expected)
			}
		})
	}
}

func TestTruncateLeft(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		prefix   string
		expected string
	}{
		{"short string unchanged", "hello", 10, "...", "hello"},
		{"truncation from left", "hello world", 8, "...", "...world"},
		{"multi-byte UTF-8", "你好世界测试", 3, "...", "界测试"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TruncateLeft(tt.input, tt.maxLen, tt.prefix)
			if result != tt.expected {
				t.Errorf("TruncateLeft(%q, %d, %q) = %q, want %q", tt.input, tt.maxLen, tt.prefix, result, tt.expected)
			}
		})
	}
}
