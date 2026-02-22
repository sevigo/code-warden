package rag

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/sevigo/code-warden/internal/github"
	"github.com/stretchr/testify/assert"
)

func TestParseHunkStartLine(t *testing.T) {
	tests := []struct {
		name       string
		hunkHeader string
		want       int
	}{
		{
			name:       "standard hunk header",
			hunkHeader: "@@ -1,5 +10,7 @@",
			want:       10,
		},
		{
			name:       "hunk header with single line",
			hunkHeader: "@@ -5 +20 @@",
			want:       20,
		},
		{
			name:       "hunk header with context",
			hunkHeader: "@@ -100,15 +200,25 @@ func someFunction()",
			want:       200,
		},
		{
			name:       "empty hunk header",
			hunkHeader: "",
			want:       0,
		},
		{
			name:       "invalid hunk header",
			hunkHeader: "@@ invalid @@",
			want:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHunkStartLine(tt.hunkHeader)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractNewFunctions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil))
	detector := NewReuseDetector(nil, nil, nil, logger)

	tests := []struct {
		name     string
		file     github.ChangedFile
		expected int
	}{
		{
			name: "single new function",
			file: github.ChangedFile{
				Filename: "test.go",
				Patch: `@@ -0,0 +1,10 @@
+package test
+
+func SanitizeEmail(email string) string {
+	return strings.TrimSpace(strings.ToLower(email))
+}
+`,
			},
			expected: 1,
		},
		{
			name: "multiple new functions",
			file: github.ChangedFile{
				Filename: "utils.go",
				Patch: `@@ -0,0 +1,20 @@
+package utils
+
+func SanitizeEmail(email string) string {
+	return strings.TrimSpace(strings.ToLower(email))
+}
+
+func ValidateEmail(email string) bool {
+	re := regexp.MustCompile("^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}$")
+	return re.MatchString(email)
+}
+`,
			},
			expected: 2,
		},
		{
			name: "small function skipped",
			file: github.ChangedFile{
				Filename: "small.go",
				Patch: `@@ -0,0 +1,3 @@
+package small
+
+func GetName() string { return "name" }
+`,
			},
			expected: 0, // Too small, should be skipped
		},
		{
			name: "no patch content",
			file: github.ChangedFile{
				Filename: "empty.go",
				Patch:    "",
			},
			expected: 0,
		},
		{
			name: "method with receiver",
			file: github.ChangedFile{
				Filename: "service.go",
				Patch: `@@ -0,0 +1,10 @@
+package service
+
+func (s *Service) ProcessData(data string) error {
+	if data == "" {
+		return errors.New("empty data")
+	}
+	return s.process(data)
+}
+`,
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			funcs := detector.extractNewFunctions(tt.file)
			assert.Len(t, funcs, tt.expected)
		})
	}
}

func TestExtractFunctionBody(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(nil, nil))
	detector := NewReuseDetector(nil, nil, nil, logger)

	patch := []string{
		"@@ -0,0 +1,15 @@",
		"+package test",
		"+",
		"+func ComplexFunction(x int) int {",
		"+	if x < 0 {",
		"+		return -x",
		"+	}",
		"+	result := 0",
		"+	for i := 0; i < x; i++ {",
		"+		result += i",
		"+	}",
		"+	return result",
		"+}",
	}

	body := detector.extractFunctionBody(patch, 3)

	// Should capture lines starting with + or space within the function
	assert.NotEmpty(t, body)
	assert.Contains(t, strings.Join(body, "\n"), "ComplexFunction")
}

func TestVerificationOutputParser(t *testing.T) {
	parser := &verificationOutputParser{}

	tests := []struct {
		name     string
		output   string
		expected verificationResult
	}{
		{
			name:   "valid JSON response",
			output: `Some text before {"is_redundant": true, "confidence": 0.85, "reason": "same functionality", "suggestion": "Use existing function"} some text after`,
			expected: verificationResult{
				IsRedundant: true,
				Confidence:  0.85,
				Reason:      "same functionality",
				Suggestion:  "Use existing function",
			},
		},
		{
			name:   "no JSON in response",
			output: "This is just some text without any JSON",
			expected: verificationResult{
				IsRedundant: false,
				Confidence:  0,
				Reason:      "failed to parse verification response",
			},
		},
		{
			name:   "invalid JSON",
			output: `{"is_redundant": true, invalid json here}`,
			expected: verificationResult{
				IsRedundant: false,
				Confidence:  0,
				Reason:      "parse error:",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parser.Parse(context.TODO(), tt.output)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected.IsRedundant, result.IsRedundant)
			assert.Equal(t, tt.expected.Confidence, result.Confidence)
			if tt.expected.Reason != "parse error:" {
				assert.Equal(t, tt.expected.Reason, result.Reason)
			} else {
				assert.Contains(t, result.Reason, "parse error:")
			}
		})
	}
}

func TestIntentOutputParser(t *testing.T) {
	parser := &intentOutputParser{}

	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "simple intent",
			output:   "Validates an email address using regex",
			expected: "Validates an email address using regex",
		},
		{
			name:     "intent with whitespace",
			output:   "  Validates an email address  ",
			expected: "Validates an email address",
		},
		{
			name:     "multiline intent",
			output:   "Validates\nan email",
			expected: "Validates\nan email",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parser.Parse(context.TODO(), tt.output)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
