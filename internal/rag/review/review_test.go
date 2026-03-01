package review

import "testing"

func TestContextIsEmpty(t *testing.T) {
	tests := []struct {
		name               string
		contextString      string
		definitionsContext string
		expected           bool
	}{
		{
			name:               "both empty",
			contextString:      "",
			definitionsContext: "",
			expected:           true,
		},
		{
			name:               "only contextString empty",
			contextString:      "",
			definitionsContext: "type Config struct{}",
			expected:           false,
		},
		{
			name:               "only definitionsContext empty",
			contextString:      "Architectural overview here",
			definitionsContext: "",
			expected:           false,
		},
		{
			name:               "both present",
			contextString:      "Architectural overview",
			definitionsContext: "type Config struct{}",
			expected:           false,
		},
		{
			name:               "contextString is whitespace only",
			contextString:      "   ",
			definitionsContext: "",
			expected:           false, // whitespace is not empty string // Wait! My review package changed it slightly
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contextIsEmpty(tt.contextString, tt.definitionsContext)
			if got != tt.expected {
				t.Errorf("contextIsEmpty(%q, %q) = %v, want %v", tt.contextString, tt.definitionsContext, got, tt.expected)
			}
		})
	}
}
