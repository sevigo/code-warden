package jobs

import (
	"log/slog"
	"os"
	"testing"

	"github.com/sevigo/code-warden/internal/core"
)

func TestValidateSuggestions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	validFiles := map[string]struct{}{
		"main.go":       {},
		"pkg/util.go":   {},
		"cmd/server.go": {},
	}

	tests := []struct {
		name        string
		suggestions []core.Suggestion
		wantLen     int
	}{
		{
			name: "All valid",
			suggestions: []core.Suggestion{
				{FilePath: "main.go"},
				{FilePath: "pkg/util.go"},
			},
			wantLen: 2,
		},
		{
			name: "Mix valid and invalid",
			suggestions: []core.Suggestion{
				{FilePath: "main.go"},
				{FilePath: "invalid.go"},
				{FilePath: "pkg/util.go"},
				{FilePath: "src/old.java"},
			},
			wantLen: 2,
		},
		{
			name: "With ./ prefix",
			suggestions: []core.Suggestion{
				{FilePath: "./main.go"},
				{FilePath: "pkg/util.go"},
			},
			wantLen: 2,
		},
		{
			name: "Empty valid files (should skip validation/return empty?) - Current implementation logs warn and returns empty list if validFiles is NOT empty",
			// Wait, implementation says: if len(validFiles) == 0 -> return suggestions (skip validation)
			// But here we pass validFiles map which IS populated.
			// So if we pass a suggestion that is NOT in validFiles, it should be dropped.
			suggestions: []core.Suggestion{
				{FilePath: "ghost.go"},
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateSuggestions(logger, tt.suggestions, validFiles)
			if len(got) != tt.wantLen {
				t.Errorf("validateSuggestions() got %d suggestions, want %d", len(got), tt.wantLen)
			}
		})
	}

	t.Run("No valid files provided", func(t *testing.T) {
		got := validateSuggestions(logger, []core.Suggestion{{FilePath: "any.go"}}, nil)
		if len(got) != 1 {
			t.Errorf("expected validation to be skipped when no valid files provided, got %d", len(got))
		}
	})
}
