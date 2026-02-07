package llm

import (
	"encoding/json"
	"reflect"
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
		input      string
		wantPrefix string
	}{
		{"kimi-k2.5:cloud", "kimi-k2.5_cloud_"},
		{"deepseek/v3", "deepseek_v3_"},
		{"suspicious..name", "suspicious..name_"},
		{"<invalid>", "invalid_"},
		{"COM1", "safe_COM1_"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeModelForFilename(tt.input)
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("SanitizeModelForFilename(%q) = %q, want prefix %q", tt.input, got, tt.wantPrefix)
			}
			// Check if hash part is exactly 8 hex chars (plus underscore)
			hashPart := got[len(tt.wantPrefix):]
			if len(hashPart) != 8 {
				t.Errorf("SanitizeModelForFilename(%q) hash part %q length = %d, want 8", tt.input, hashPart, len(hashPart))
			}
		})
	}

	t.Run("CollisionResistance", func(t *testing.T) {
		m1 := SanitizeModelForFilename("model:v1")
		m2 := SanitizeModelForFilename("model/v1")
		if m1 == m2 {
			t.Errorf("Collision detected: %q and %q both sanitize to %q", "model:v1", "model/v1", m1)
		}
	})
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

			if !tt.shouldErr {
				var gotVal, wantVal any
				if err := json.Unmarshal([]byte(got), &gotVal); err != nil {
					t.Fatalf("extractJSON returned invalid JSON: %v", err)
				}
				if err := json.Unmarshal([]byte(tt.want), &wantVal); err != nil {
					t.Fatalf("test expectation is invalid JSON: %v", err)
				}
				if !reflect.DeepEqual(gotVal, wantVal) {
					t.Errorf("extractJSON() semantic mismatch for %s", tt.name)
					t.Logf("got:  %s", got)
					t.Logf("want: %s", tt.want)
				}
			}
		})
	}
}
