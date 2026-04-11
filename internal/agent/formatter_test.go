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

func TestFormatter_NilReceiver(t *testing.T) {
	var f *Formatter
	if err := f.Format(context.Background(), "/tmp", "/tmp/test.go"); err != nil {
		t.Errorf("nil Formatter should be a no-op: %v", err)
	}
}

func TestFormatter_Format_SkipsUnknownExtension(t *testing.T) {
	f := NewFormatter(slog.Default())
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Format(context.Background(), dir, filePath); err != nil {
		t.Errorf("Format should skip unknown extensions: %v", err)
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

	err := f.Format(context.Background(), dir, filePath)
	if err != nil {
		t.Fatalf("Format failed: %v", err)
	}

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

	err := f.Format(context.Background(), dir, filePath)
	if err != nil {
		t.Fatalf("Format failed: %v", err)
	}
}

func TestFormatter_FormatsPythonWithRuff(t *testing.T) {
	if _, err := exec.LookPath("ruff"); err != nil {
		t.Skip("ruff not available")
	}
	f := NewFormatter(slog.Default())
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.py")
	content := "x=1\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := f.Format(context.Background(), dir, filePath)
	if err != nil {
		t.Fatalf("Format failed: %v", err)
	}
}
