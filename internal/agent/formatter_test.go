package agent

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	content := "package test\n\nimport \"fmt\"\n\nfunc Foo() {fmt.Println(\"hi\")}\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	f.Format(context.Background(), dir, filePath)
}

func TestFormatter_FormatProject_EmptyCommand(_ *testing.T) {
	var f *Formatter
	f.FormatProject(context.Background(), "/tmp", "")
}

func TestFormatter_FormatProject_RunsCommand(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo not available")
	}
	f := NewFormatter(slog.Default())
	dir := t.TempDir()
	f.FormatProject(context.Background(), dir, "echo formatted")
}
