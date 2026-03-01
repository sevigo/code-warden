package review

import (
	"strings"
	"testing"
)

func TestParseDiff_SkipsDiffMetadata(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
index 1234567..abcdef0 100644
--- a/main.go
+++ b/main.go
@@ -1,5 +1,6 @@
 package main
 
+import "fmt"
+
 func main() {
+	fmt.Println("hello")
 }
`
	files := ParseDiff(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Filename != "main.go" {
		t.Errorf("expected filename 'main.go', got %q", files[0].Filename)
	}
	// Patch should NOT contain --- or +++ lines
	patch := files[0].Patch
	if containsLine(patch, "--- a/main.go") {
		t.Error("patch should not contain '--- a/main.go'")
	}
	if containsLine(patch, "+++ b/main.go") {
		t.Error("patch should not contain '+++ b/main.go'")
	}
	// But should contain actual code lines
	if !containsLine(patch, "+import \"fmt\"") {
		t.Error("patch should contain '+import \"fmt\"'")
	}
	if !containsLine(patch, " package main") {
		t.Error("patch should contain context line ' package main'")
	}
}

func TestParseDiff_MultipleFiles(t *testing.T) {
	diff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package foo
+var x = 1
diff --git a/bar.go b/bar.go
--- a/bar.go
+++ b/bar.go
@@ -1 +1,2 @@
 package bar
+var y = 2
`
	files := ParseDiff(diff)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Filename != "foo.go" {
		t.Errorf("expected 'foo.go', got %q", files[0].Filename)
	}
	if files[1].Filename != "bar.go" {
		t.Errorf("expected 'bar.go', got %q", files[1].Filename)
	}
}

func TestParseDiff_EmptyDiff(t *testing.T) {
	files := ParseDiff("")
	if len(files) != 0 {
		t.Errorf("expected 0 files for empty diff, got %d", len(files))
	}
}

func containsLine(text, line string) bool {
	for _, l := range splitLines(text) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(text string) []string {
	var lines []string
	start := 0
	for i := range len(text) {
		if text[i] == '\n' {
			lines = append(lines, text[start:i])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
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
			// Check if hash part is exactly 16 hex chars (plus underscore)
			hashPart := got[len(tt.wantPrefix):]
			if len(hashPart) != 16 {
				t.Errorf("SanitizeModelForFilename(%q) hash part %q length = %d, want 16", tt.input, hashPart, len(hashPart))
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
