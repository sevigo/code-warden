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
			name: "Unknown file is dropped",
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

func TestIsReviewableFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		// Code files - should be reviewable
		{"main.go", true},
		{"app.py", true},
		{"index.ts", true},
		{"Component.tsx", true},
		{"App.java", true},
		{"main.rs", true},
		{"lib.rb", true},
		{"index.php", true},
		{"Program.cs", true},
		{"ViewController.swift", true},
		{"Main.kt", true},
		{"script.sh", true},
		{"query.sql", true},
		{"Component.vue", true},

		// Documentation - not reviewable
		{"README.md", false},
		{"CHANGELOG.markdown", false},
		{"docs/guide.rst", false},
		{"INSTALL.adoc", false},

		// Configuration - not reviewable
		{"config.yaml", false},
		{"docker-compose.yml", false},
		{"package.json", false},
		{"tsconfig.json", false},
		{"settings.toml", false},
		{".env", false},
		{".gitignore", false},

		// Lock files - not reviewable
		{"go.sum", false},
		{"package-lock.json", false},
		{"yarn.lock", false},

		// Data files - not reviewable
		{"data.txt", false},
		{"records.csv", false},
		{"config.xml", false},

		// Binary/Assets - not reviewable
		{"logo.png", false},
		{"icon.svg", false},
		{"banner.jpg", false},
		{"document.pdf", false},

		// Templates/Prompts - not reviewable
		{"review.prompt", false},
		{"email.tmpl", false},

		// Unknown extensions - reviewable (err on side of review)
		{"schema.graphql", true},
		{"api.proto", true},
		{"Dockerfile", false}, // build file without extension
		{"Makefile", false},   // build file without extension

		// Edge cases
		{"./main.go", true},       // with ./ prefix
		{"src/lib/main.go", true}, // nested path
		{"bundle.min.js", false},  // minified
		{"styles.min.css", false}, // minified
		{"types.d.ts", false},     // TypeScript declarations
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := isReviewableFile(tt.path)
			if result != tt.expected {
				t.Errorf("isReviewableFile(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestFilterNonCodeSuggestions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	tests := []struct {
		name     string
		input    []core.Suggestion
		expected int
	}{
		{
			name: "filters out markdown files",
			input: []core.Suggestion{
				{FilePath: "README.md", LineNumber: 10},
				{FilePath: "main.go", LineNumber: 5},
			},
			expected: 1,
		},
		{
			name: "filters out yaml and json files",
			input: []core.Suggestion{
				{FilePath: "config.yaml", LineNumber: 1},
				{FilePath: "package.json", LineNumber: 1},
				{FilePath: "app.go", LineNumber: 1},
			},
			expected: 1,
		},
		{
			name: "keeps code files",
			input: []core.Suggestion{
				{FilePath: "main.go", LineNumber: 1},
				{FilePath: "app.py", LineNumber: 1},
				{FilePath: "index.ts", LineNumber: 1},
				{FilePath: "App.java", LineNumber: 1},
			},
			expected: 4,
		},
		{
			name: "filters out lock and sum files",
			input: []core.Suggestion{
				{FilePath: "go.sum", LineNumber: 1},
				{FilePath: "package-lock.json", LineNumber: 1},
				{FilePath: "main.go", LineNumber: 1},
			},
			expected: 1,
		},
		{
			name: "filters out image files",
			input: []core.Suggestion{
				{FilePath: "logo.png", LineNumber: 1},
				{FilePath: "icon.svg", LineNumber: 1},
				{FilePath: "main.go", LineNumber: 1},
			},
			expected: 1,
		},
		{
			name: "handles ./ prefix",
			input: []core.Suggestion{
				{FilePath: "./README.md", LineNumber: 1},
				{FilePath: "./main.go", LineNumber: 1},
			},
			expected: 1,
		},
		{
			name: "keeps unknown extensions",
			input: []core.Suggestion{
				{FilePath: "schema.graphql", LineNumber: 1},
				{FilePath: "service.proto", LineNumber: 1},
			},
			expected: 2,
		},
		{
			name: "filters out minified files",
			input: []core.Suggestion{
				{FilePath: "bundle.min.js", LineNumber: 1},
				{FilePath: "styles.min.css", LineNumber: 1},
				{FilePath: "app.js", LineNumber: 1},
			},
			expected: 1,
		},
		{
			name:     "empty input returns empty",
			input:    []core.Suggestion{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterNonCodeSuggestions(logger, tt.input)
			if len(result) != tt.expected {
				t.Errorf("FilterNonCodeSuggestions: got %d, want %d", len(result), tt.expected)
			}
		})
	}
}
