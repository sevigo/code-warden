package reviewcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test if git is not on PATH.
func requireGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestGitDiffUncommitted(t *testing.T) {
	requireGit(t)

	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")

	// Create a tracked file with a committed change.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")

	// Uncommitted modification.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff, err := GitDiff(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("GitDiff: %v", err)
	}
	if diff == "" {
		t.Error("expected a non-empty diff for uncommitted changes")
	}
	if !strings.Contains(diff, "file.txt") {
		t.Errorf("diff should mention file.txt:\n%s", diff)
	}
}

func TestGitDiffEmpty(t *testing.T) {
	requireGit(t)

	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	diff, err := GitDiff(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("GitDiff: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff on a clean tree, got:\n%s", diff)
	}
}

func TestChangedFilesFromDiff(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n" +
		"index 000..111\n" +
		"--- a/a.go\n" +
		"+++ b/a.go\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+func A() {}\n" +
		"+func B() {}\n" +
		"diff --git a/b.go b/b.go\n" +
		"index 000..222\n" +
		"--- a/b.go\n" +
		"+++ b/b.go\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"

	files := ChangedFilesFromDiff(diff)
	if len(files) != 2 {
		t.Fatalf("expected 2 changed files, got %d", len(files))
	}
	if files[0].Filename != "a.go" || files[1].Filename != "b.go" {
		t.Errorf("unexpected filenames: %v, %v", files[0].Filename, files[1].Filename)
	}
	if !strings.Contains(files[0].Patch, "func A") {
		t.Errorf("a.go patch missing content: %q", files[0].Patch)
	}
}

func TestGitLog(t *testing.T) {
	requireGit(t)

	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feat: add a.txt")

	msgs, err := GitLog(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("GitLog: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected at least one commit message")
	}
	if !strings.Contains(msgs[0], "add a.txt") {
		t.Errorf("unexpected first commit message: %q", msgs[0])
	}
}

// runGit runs a git command in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, string(out))
	}
}
