package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sevigo/code-warden/internal/core"
)

func TestFormatter_NilReceiver(_ *testing.T) {
	var f *Formatter
	f.Format(context.Background(), "/tmp", "/tmp/test.go")
}

func TestFormatter_Format_SkipsNonGo(t *testing.T) {
	f := NewFormatter(slog.Default())
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.py")
	if err := os.WriteFile(filePath, []byte("x=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.Format(context.Background(), dir, filePath)
	data, _ := os.ReadFile(filePath)
	if string(data) != "x=1\n" {
		t.Error("non-Go files should not be formatted")
	}
}

func TestFormatter_Format_FormatsGoFile(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not available")
	}
	f := NewFormatter(slog.Default())
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.go")
	content := "package test\n\nfunc  Foo ( )  { }\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	f.Format(context.Background(), dir, filePath)

	formatted, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(formatted), "func  Foo ( )") {
		t.Error("gofmt did not format the file")
	}
}

func TestFormatter_Format_PrefersGoimports(t *testing.T) {
	if _, err := exec.LookPath("goimports"); err != nil {
		t.Skip("goimports not available")
	}
	f := NewFormatter(slog.Default())
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.go")
	content := "package test\n\nfunc Foo(){}\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	f.Format(context.Background(), dir, filePath)

	formatted, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(formatted), "func Foo(){}") {
		t.Error("goimports did not format the file")
	}
}

func TestFormatter_FormatProject_NilReceiverReturnsFalse(t *testing.T) {
	var f *Formatter
	if f.FormatProject(context.Background(), "/tmp", "echo hi") {
		t.Error("nil receiver should return false")
	}
}

func TestFormatter_FormatProject_EmptyCommandReturnsFalse(t *testing.T) {
	f := NewFormatter(slog.Default())
	if f.FormatProject(context.Background(), "/tmp", "") {
		t.Error("empty command should return false")
	}
}

func TestFormatter_FormatProject_SuccessReturnsTrue(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo not available")
	}
	f := NewFormatter(slog.Default())
	if !f.FormatProject(context.Background(), t.TempDir(), "echo formatted") {
		t.Error("successful command should return true")
	}
}

func TestFormatter_FormatProject_FailureReturnsFalse(t *testing.T) {
	f := NewFormatter(slog.Default())
	// "false" exits with code 1 on all Unix systems.
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("false not available")
	}
	if f.FormatProject(context.Background(), t.TempDir(), "false") {
		t.Error("failing command should return false")
	}
}

func TestNewFormatterFromConfig_Disabled(t *testing.T) {
	cfg := &core.RepoConfig{DisableFormatOnWrite: true}
	if newFormatterFromConfig(slog.Default(), cfg) != nil {
		t.Error("expected nil formatter when DisableFormatOnWrite is true")
	}
}

func TestNewFormatterFromConfig_Enabled(t *testing.T) {
	cfg := &core.RepoConfig{}
	if newFormatterFromConfig(slog.Default(), cfg) == nil {
		t.Error("expected non-nil formatter when DisableFormatOnWrite is false")
	}
}

func TestNewFormatterFromConfig_NilConfig(t *testing.T) {
	if newFormatterFromConfig(slog.Default(), nil) == nil {
		t.Error("expected non-nil formatter when config is nil")
	}
}
