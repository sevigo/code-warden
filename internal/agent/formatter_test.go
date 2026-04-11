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

func TestFormatter_FormatProject_EmptyCommand(_ *testing.T) {
	var f *Formatter
	f.FormatProject(context.Background(), "/tmp", "")
}

func TestFormatter_FormatProject_MultiWordCommand(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo not available")
	}
	f := NewFormatter(slog.Default())
	dir := t.TempDir()
	testFile := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Use a multi-word command that strings.Fields splits correctly.
	// "touch" with a path that has spaces would also work, but "echo" is simpler.
	f.FormatProject(context.Background(), dir, "echo formatted")
	// The command should execute without error; we just verify it doesn't crash.
}

func TestStringsFieldsSplitsMultiWordCommand(t *testing.T) {
	parts := strings.Fields("npm run format")
	if len(parts) != 3 || parts[0] != "npm" || parts[1] != "run" || parts[2] != "format" {
		t.Errorf("strings.Fields splits incorrectly: %v", parts)
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
