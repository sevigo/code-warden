package llm

import (
	"encoding/json"
	"reflect"
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
		{"kimi-k2.5:cloud", "kimi_k2_5_cloud"},
		{"deepseek/v3", "deepseek_v3"},
		{"suspicious..name", "suspicious_name"},
		{"<invalid>", "invalid"},
		{"COM1", "safe_COM1"},
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
