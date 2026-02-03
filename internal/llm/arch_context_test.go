package llm

import (
	"testing"

	"github.com/sevigo/goframe/schema"
)

func TestGetDirectoryPath(t *testing.T) {
	r := &ragService{}

	tests := []struct {
		source   string
		expected string
	}{
		{"internal/llm/rag.go", "internal/llm"},
		{"internal\\llm\\rag.go", "internal/llm"},
		{"main.go", "root"},
		{"", ""},
		{"./local_file.go", "root"},
		{".\\local_file.go", "root"},
	}

	for _, tt := range tests {
		doc := schema.Document{
			Metadata: map[string]any{"source": tt.source},
		}
		actual := r.getDirectoryPath(doc)
		if actual != tt.expected {
			t.Errorf("source: %s, expected: %s, got: %s", tt.source, tt.expected, actual)
		}
	}
}

func TestCalculateDirectoryHash(t *testing.T) {
	info1 := &DirectoryInfo{
		Files:   []string{"a.go", "b.go"},
		Symbols: []string{"FuncA", "FuncB"},
	}
	info2 := &DirectoryInfo{
		Files:   []string{"a.go", "b.go"},
		Symbols: []string{"FuncA", "FuncB"},
	}
	info3 := &DirectoryInfo{
		Files:   []string{"a.go"},
		Symbols: []string{"FuncA"},
	}

	hash1 := calculateDirectoryHash(info1)
	hash2 := calculateDirectoryHash(info2)
	hash3 := calculateDirectoryHash(info3)

	if hash1 != hash2 {
		t.Errorf("expected same hash for same content, got %s and %s", hash1, hash2)
	}
	if hash1 == hash3 {
		t.Errorf("expected different hash for different content, both got %s", hash1)
	}
	if len(hash1) != 16 { // 8 bytes -> 16 hex chars
		t.Errorf("expected 16 chars hash, got %d", len(hash1))
	}
}

func TestExtractDocMetadata(t *testing.T) {
	r := &ragService{}
	info := &DirectoryInfo{
		Files:   []string{},
		Symbols: []string{},
	}

	doc := schema.Document{
		Metadata: map[string]any{
			"source":     "pkg/foo.go",
			"identifier": "FooFunc",
			"chunk_type": "func",
		},
	}

	r.extractDocMetadata(doc, info)

	if len(info.Files) != 1 || info.Files[0] != "foo.go" {
		t.Errorf("expected foo.go in Files, got %v", info.Files)
	}
	if !containsString(info.Symbols, "FooFunc") {
		t.Errorf("expected FooFunc in Symbols")
	}
	if !containsString(info.Symbols, "func: FooFunc") {
		t.Errorf("expected func: FooFunc in Symbols")
	}
}
