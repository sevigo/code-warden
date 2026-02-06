package llm

import (
	"strings"
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

func TestExtractJSON(t *testing.T) {
	r := &ragService{}

	tests := []struct {
		name      string
		input     string
		want      string
		shouldErr bool
	}{
		{
			name:  "Clean JSON",
			input: `{"key": "value"}`,
			want:  `{"key":"value"}`,
		},
		{
			name:  "JSON with Preamble",
			input: `Here is the JSON: {"key": "value"}`,
			want:  `{"key":"value"}`,
		},
		{
			name:  "JSON with Trailing Text",
			input: `{"key": "value"} ... and some explanation`,
			want:  `{"key":"value"}`,
		},
		{
			name:  "Nested Braces in String",
			input: `{"path": "C:\\Users\\{app}"}`,
			want:  `{"path":"C:\\Users\\{app}"}`,
		},
		{
			name:  "Escaped Quotes",
			input: `{"summary": "He said \"Hello\""}`,
			want:  `{"summary":"He said \"Hello\""}`,
		},
		{
			name:      "Invalid JSON",
			input:     `not json`,
			shouldErr: true,
		},
		{
			name:      "Incomplete JSON",
			input:     `{"key": "value"`,
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.extractJSON(tt.input)
			if (err != nil) != tt.shouldErr {
				t.Errorf("extractJSON() error = %v, shouldErr %v", err, tt.shouldErr)
				return
			}
			// Normalize for Windows/whitespace differences
			normalize := func(s string) string {
				return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", "")
			}
			if !tt.shouldErr && normalize(got) != normalize(tt.want) {
				t.Errorf("extractJSON() mismatch for %s", tt.name)
				t.Logf("got:  %q", got)
				t.Logf("want: %q", tt.want)
			}
		})
	}
}
