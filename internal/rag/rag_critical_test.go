package rag

import (
	"testing"
	"time"
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

// --- ttlCache tests ---

func TestTTLCache_BasicStoreLoad(t *testing.T) {
	c := newTTLCache(1*time.Minute, 10)
	c.Store("key1", "value1")

	v, ok := c.Load("key1")
	if !ok {
		t.Fatal("expected key1 to be found")
	}
	if v.(string) != "value1" {
		t.Errorf("expected 'value1', got %q", v)
	}
}

func TestTTLCache_MissReturnsNotFound(t *testing.T) {
	c := newTTLCache(1*time.Minute, 10)
	_, ok := c.Load("missing")
	if ok {
		t.Error("expected missing key to not be found")
	}
}

func TestTTLCache_ExpiredEntryEvicted(t *testing.T) {
	c := newTTLCache(1*time.Millisecond, 10)
	c.Store("key1", "value1")
	time.Sleep(5 * time.Millisecond)

	_, ok := c.Load("key1")
	if ok {
		t.Error("expected expired entry to be evicted on Load")
	}
}

func TestTTLCache_MaxSizeEvictsOldest(t *testing.T) {
	c := newTTLCache(1*time.Hour, 3) // small cache
	c.Store("a", "1")
	c.Store("b", "2")
	c.Store("c", "3")
	// At capacity — this should evict "a" (oldest)
	c.Store("d", "4")

	if _, ok := c.Load("a"); ok {
		t.Error("expected 'a' to be evicted (oldest)")
	}
	if _, ok := c.Load("d"); !ok {
		t.Error("expected 'd' to be present")
	}
}

func TestTTLCache_OverwriteExistingKey(t *testing.T) {
	c := newTTLCache(1*time.Minute, 10)
	c.Store("key", "v1")
	c.Store("key", "v2")

	v, ok := c.Load("key")
	if !ok {
		t.Fatal("expected key to be found")
	}
	if v.(string) != "v2" {
		t.Errorf("expected 'v2', got %q", v)
	}
}
