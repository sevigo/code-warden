package llm

import (
	"testing"
)

func TestSanitizeJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Valid JSON",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
	}

	// We can't access private methods from external test package unless it's in the same package
	// So we assume this test file is in package llm

	r := &ragService{} // Dummy receiver

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.sanitizeJSON(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeJSON(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeModelForFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"kimi-k2.5:cloud", "kimi-k2_5_cloud"},
		{"deepseek/v3", "deepseek_v3"},
		// Wait, current logic allows a-z A-Z 0-9 - _
		// So .. would technically become __ in the strict allowlist version I implemented?
		// Let's check the implementation I wrote:
		// case r >= 'a' && r <= 'z': return r ... default: return '_'
		// So '.' becomes '_'
		{"suspicious..name", "suspicious__name"},
		{"<invalid>", "_invalid_"},
		{"COM1", "COM1"}, // Windows reserved names are not handled by char replacement, but that's okay for now
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeModelForFilename(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeModelForFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
