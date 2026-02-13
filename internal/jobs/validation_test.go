package jobs

import (
	"log/slog"
	"os"
	"testing"

	"github.com/sevigo/code-warden/internal/core"
)

func TestValidateSuggestionsByLine(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	validFiles := map[string]map[int]struct{}{
		"main.go":       {1: {}, 10: {}, 20: {}},
		"pkg/util.go":   {1: {}, 5: {}},
		"cmd/server.go": {1: {}},
	}

	tests := []struct {
		name           string
		suggestions    []core.Suggestion
		wantInlineLen  int
		wantOffDiffLen int
	}{
		{
			name: "All valid inline",
			suggestions: []core.Suggestion{
				{FilePath: "main.go", LineNumber: 1},
				{FilePath: "pkg/util.go", LineNumber: 1},
			},
			wantInlineLen:  2,
			wantOffDiffLen: 0,
		},
		{
			name: "Mix valid and unknown file",
			suggestions: []core.Suggestion{
				{FilePath: "main.go", LineNumber: 1},
				{FilePath: "invalid.go", LineNumber: 1},
				{FilePath: "pkg/util.go", LineNumber: 1},
				{FilePath: "src/old.java", LineNumber: 1},
			},
			wantInlineLen:  2,
			wantOffDiffLen: 0,
		},
		{
			name: "With ./ prefix",
			suggestions: []core.Suggestion{
				{FilePath: "./main.go", LineNumber: 1},
				{FilePath: "pkg/util.go", LineNumber: 1},
			},
			wantInlineLen:  2,
			wantOffDiffLen: 0,
		},
		{
			name: "Valid file but off-diff line",
			suggestions: []core.Suggestion{
				{FilePath: "main.go", LineNumber: 999},
			},
			wantInlineLen:  0,
			wantOffDiffLen: 1,
		},
		{
			name: "Mix inline and off-diff",
			suggestions: []core.Suggestion{
				{FilePath: "main.go", LineNumber: 1},
				{FilePath: "main.go", LineNumber: 999},
				{FilePath: "pkg/util.go", LineNumber: 5},
				{FilePath: "pkg/util.go", LineNumber: 100},
			},
			wantInlineLen:  2,
			wantOffDiffLen: 2,
		},
		{
			name: "Zero line number goes to off-diff",
			suggestions: []core.Suggestion{
				{FilePath: "main.go", LineNumber: 0},
			},
			wantInlineLen:  0,
			wantOffDiffLen: 1,
		},
		{
			name: "Unknown file is dropped entirely",
			suggestions: []core.Suggestion{
				{FilePath: "ghost.go", LineNumber: 1},
			},
			wantInlineLen:  0,
			wantOffDiffLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inline, offDiff := ValidateSuggestionsByLine(logger, tt.suggestions, validFiles)
			if len(inline) != tt.wantInlineLen {
				t.Errorf("inline: got %d, want %d", len(inline), tt.wantInlineLen)
			}
			if len(offDiff) != tt.wantOffDiffLen {
				t.Errorf("offDiff: got %d, want %d", len(offDiff), tt.wantOffDiffLen)
			}
		})
	}

	t.Run("No valid files provided", func(t *testing.T) {
		inline, offDiff := ValidateSuggestionsByLine(logger, []core.Suggestion{{FilePath: "any.go"}}, nil)
		if len(inline) != 1 {
			t.Errorf("expected validation to be skipped when no valid files provided, got %d", len(inline))
		}
		if len(offDiff) != 0 {
			t.Errorf("expected no off-diff when validation is skipped, got %d", len(offDiff))
		}
	})
}
