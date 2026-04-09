package lsp

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestFormatDiagnostics_Empty verifies empty diagnostics returns ok:true.
func TestFormatDiagnostics_Empty(t *testing.T) {
	t.Parallel()
	result := formatDiagnostics(nil)
	if result["ok"] != true {
		t.Errorf("expected ok=true for empty diagnostics, got %v", result["ok"])
	}
	if result["count"] != 0 {
		t.Errorf("expected count=0, got %v", result["count"])
	}
}

// TestFormatDiagnostics_ErrorSeverity verifies that error-severity diagnostics set ok=false.
func TestFormatDiagnostics_ErrorSeverity(t *testing.T) {
	t.Parallel()
	diags := []Diagnostic{
		{Severity: SeverityError, Message: "undefined: Foo"},
	}
	result := formatDiagnostics(diags)
	if result["ok"] != false {
		t.Errorf("expected ok=false for error-severity diagnostic, got %v", result["ok"])
	}
	if result["count"] != 1 {
		t.Errorf("expected count=1, got %v", result["count"])
	}
}

// TestFormatDiagnostics_WarningSeverity verifies warnings do not flip ok to false.
func TestFormatDiagnostics_WarningSeverity(t *testing.T) {
	t.Parallel()
	diags := []Diagnostic{
		{Severity: SeverityWarning, Message: "unused variable x"},
	}
	result := formatDiagnostics(diags)
	if result["ok"] != true {
		t.Errorf("expected ok=true for warning-only diagnostics, got %v", result["ok"])
	}
}

// TestFormatDiagnostics_MixedSeverity verifies that any error flips ok to false
// even when warnings are present.
func TestFormatDiagnostics_MixedSeverity(t *testing.T) {
	t.Parallel()
	diags := []Diagnostic{
		{Severity: SeverityWarning, Message: "style: use camelCase"},
		{Severity: SeverityError, Message: "cannot use int as string"},
		{Severity: SeverityHint, Message: "consider using fmt.Errorf"},
	}
	result := formatDiagnostics(diags)
	if result["ok"] != false {
		t.Errorf("expected ok=false when any diagnostic is SeverityError, got %v", result["ok"])
	}
	if result["count"] != 3 {
		t.Errorf("expected count=3, got %v", result["count"])
	}
}

// TestFormatDiagnostics_HintOnly verifies hints and info alone keep ok=true.
func TestFormatDiagnostics_HintOnly(t *testing.T) {
	t.Parallel()
	diags := []Diagnostic{
		{Severity: SeverityHint, Message: "consider renaming"},
		{Severity: SeverityInformation, Message: "more context"},
	}
	result := formatDiagnostics(diags)
	if result["ok"] != true {
		t.Errorf("expected ok=true for hint/info-only diagnostics, got %v", result["ok"])
	}
}

// TestPathTraversal verifies the path traversal check used by resolveWorkspacePath.
// We extract and test the core filepath.Rel logic inline to avoid the context dependency.
func TestPathTraversal(t *testing.T) {
	t.Parallel()
	root := "/workspace"
	cases := []struct {
		relPath string
		escape  bool
	}{
		{"main.go", false},
		{"internal/pkg/foo.go", false},
		{".", false},
		{"../secret", true},
		{"../../etc/passwd", true},
		{"sub/../../etc/passwd", true},
	}
	for _, tc := range cases {
		t.Run(tc.relPath, func(t *testing.T) {
			t.Parallel()
			cleanRoot := filepath.Clean(root)
			abs := filepath.Clean(filepath.Join(cleanRoot, tc.relPath))
			rel, err := filepath.Rel(cleanRoot, abs)
			escapes := err != nil || strings.HasPrefix(rel, "..")
			if escapes != tc.escape {
				t.Errorf("path %q: escapes=%v want=%v", tc.relPath, escapes, tc.escape)
			}
		})
	}
}
