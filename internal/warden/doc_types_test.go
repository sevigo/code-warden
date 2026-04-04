package warden

import (
	"testing"
)

func TestDesignDocumentChunkContent(t *testing.T) {
	tests := []struct {
		name     string
		doc      *DesignDocument
		expected string
	}{
		{
			name: "returns summary when present",
			doc: &DesignDocument{
				Summary: "This is a summary",
				Content: "This is full content that is longer",
			},
			expected: "This is a summary",
		},
		{
			name: "truncates content when no summary",
			doc: &DesignDocument{
				Content: "short",
			},
			expected: "short",
		},
		{
			name: "truncates long content",
			doc: &DesignDocument{
				Content: string(make([]byte, 600)),
			},
			expected: string(make([]byte, 500)) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.doc.ChunkContent()
			if result != tt.expected {
				t.Errorf("ChunkContent() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDesignDocumentToMarkdown(t *testing.T) {
	doc := &DesignDocument{
		Title:   "Test Document",
		Content: "This is the content",
	}

	result := doc.ToMarkdown()
	expected := "# Test Document\n\nThis is the content"
	if result != expected {
		t.Errorf("ToMarkdown() = %q, want %q", result, expected)
	}
}

func TestValidDesignDocumentTypes(t *testing.T) {
	types := ValidDesignDocumentTypes()

	if len(types) != 4 {
		t.Errorf("ValidDesignDocumentTypes() returned %d types, want 4", len(types))
	}

	expectedTypes := map[DesignDocumentType]bool{
		DocTypeTestingPatterns: true,
		DocTypeDependencies:    true,
		DocTypeConventions:     true,
		DocTypeAPIPatterns:     true,
	}

	for _, dt := range types {
		if !expectedTypes[dt] {
			t.Errorf("Unexpected type: %s", dt)
		}
	}
}

func TestIsValidType(t *testing.T) {
	tests := []struct {
		docType  DesignDocumentType
		expected bool
	}{
		{DocTypeTestingPatterns, true},
		{DocTypeDependencies, true},
		{DocTypeConventions, true},
		{DocTypeAPIPatterns, true},
		{DesignDocumentType("invalid"), false},
		{DesignDocumentType(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.docType), func(t *testing.T) {
			result := IsValidType(tt.docType)
			if result != tt.expected {
				t.Errorf("IsValidType(%q) = %v, want %v", tt.docType, result, tt.expected)
			}
		})
	}
}

func TestExtractDocumentBlocks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{
			name:     "empty string",
			input:    "",
			expected: 0,
		},
		{
			name:     "no document tags",
			input:    "some random text",
			expected: 0,
		},
		{
			name: "single valid document",
			input: `<document>
{"type": "testing_patterns", "title": "Test"}
</document>`,
			expected: 1,
		},
		{
			name: "multiple documents",
			input: `<document>
{"type": "testing_patterns"}
</document>
<document>
{"type": "dependencies"}
</document>`,
			expected: 2,
		},
		{
			name: "design_doc tag",
			input: `<design_doc>
{"type": "conventions"}
</design_doc>`,
			expected: 1,
		},
		{
			name: "invalid JSON is skipped",
			input: `<document>
not valid json
</document>`,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := extractDocumentBlocks(tt.input)
			if len(blocks) != tt.expected {
				t.Errorf("extractDocumentBlocks() returned %d blocks, want %d", len(blocks), tt.expected)
			}
		})
	}
}

func TestParseDocumentBlock(t *testing.T) {
	docTypeMap := map[string]DesignDocumentType{
		"testing_patterns": DocTypeTestingPatterns,
		"dependencies":     DocTypeDependencies,
	}

	tests := []struct {
		name     string
		block    map[string]any
		wantNil  bool
		wantType DesignDocumentType
	}{
		{
			name:    "missing type",
			block:   map[string]any{"title": "Test"},
			wantNil: true,
		},
		{
			name:    "invalid type",
			block:   map[string]any{"type": "invalid"},
			wantNil: true,
		},
		{
			name:     "valid testing_patterns",
			block:    map[string]any{"type": "testing_patterns", "title": "Test"},
			wantNil:  false,
			wantType: DocTypeTestingPatterns,
		},
		{
			name:     "valid dependencies",
			block:    map[string]any{"type": "dependencies", "confidence": 0.95},
			wantNil:  false,
			wantType: DocTypeDependencies,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseDocumentBlock(tt.block, docTypeMap, "owner", "repo")
			if tt.wantNil && result != nil {
				t.Errorf("parseDocumentBlock() = %v, want nil", result)
			}
			if !tt.wantNil && result == nil {
				t.Errorf("parseDocumentBlock() = nil, want non-nil")
			}
			if !tt.wantNil && result.Type != tt.wantType {
				t.Errorf("parseDocumentBlock().Type = %q, want %q", result.Type, tt.wantType)
			}
		})
	}
}

func TestParseStringSlice(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected []string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "wrong type",
			input:    "not a slice",
			expected: nil,
		},
		{
			name:     "string slice",
			input:    []any{"a", "b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "mixed types",
			input:    []any{"a", 1, "b"},
			expected: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseStringSlice(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("parseStringSlice() = %v, want %v", result, tt.expected)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("parseStringSlice()[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}
