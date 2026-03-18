package contextpkg

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/sevigo/code-warden/mocks"
)

// TestDynamicSparseRetriever_GetRelevantDocuments tests the custom retriever
// that wraps vector store calls with sparse vector support.
func TestDynamicSparseRetriever_GetRelevantDocuments(t *testing.T) {
	testCases := []struct {
		name          string
		query         string
		mockSetup     func(sVS *mocks.MockScopedVectorStore)
		expectedCount int
		expectError   bool
	}{
		{
			name:  "successful retrieval with sparse vector",
			query: "func ProcessData",
			mockSetup: func(sVS *mocks.MockScopedVectorStore) {
				sVS.EXPECT().
					SimilaritySearch(gomock.Any(), "func ProcessData", 10, gomock.Any()).
					Return([]schema.Document{
						{PageContent: "doc1", Metadata: map[string]any{"source": "file1.go"}},
						{PageContent: "doc2", Metadata: map[string]any{"source": "file2.go"}},
					}, nil)
			},
			expectedCount: 2,
			expectError:   false,
		},
		{
			name:  "empty query returns empty results",
			query: "",
			mockSetup: func(sVS *mocks.MockScopedVectorStore) {
				sVS.EXPECT().
					SimilaritySearch(gomock.Any(), "", 10, gomock.Any()).
					Return([]schema.Document{}, nil)
			},
			expectedCount: 0,
			expectError:   false,
		},
		{
			name:  "strips patch noise from query",
			query: "diff --git a/file.go\nindex 123..456\n+++ b/file.go\n@@ -1 +1 @@\n-func Old()\n+func ProcessData()",
			mockSetup: func(sVS *mocks.MockScopedVectorStore) {
				// Should have git metadata stripped
				sVS.EXPECT().
					SimilaritySearch(gomock.Any(), "+func ProcessData()", 10, gomock.Any()).
					Return([]schema.Document{
						{PageContent: "func ProcessData() {}", Metadata: map[string]any{"source": "file.go"}},
					}, nil)
			},
			expectedCount: 1,
			expectError:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockSVS := mocks.NewMockScopedVectorStore(ctrl)
			if tc.mockSetup != nil {
				tc.mockSetup(mockSVS)
			}

			retriever := dynamicSparseRetriever{
				store:   mockSVS,
				numDocs: 10,
				builder: &builderImpl{cfg: Config{Logger: slog.Default()}},
			}

			docs, err := retriever.GetRelevantDocuments(context.Background(), tc.query)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, docs, tc.expectedCount)
			}
		})
	}
}

// TestBuildContextForPrompt_Deduplication tests that buildContextForPrompt
// correctly deduplicates documents by their keys
func TestBuildContextForPrompt_Deduplication(t *testing.T) {
	service := &builderImpl{cfg: Config{Logger: slog.Default()}}

	docs := []schema.Document{
		{
			PageContent: "func A() {}",
			Metadata:    map[string]any{"source": "file.go", "identifier": "A"},
		},
		{
			PageContent: "func B() {}",
			Metadata:    map[string]any{"source": "file.go", "identifier": "B"},
		},
		{
			// Duplicate of first doc - should be skipped
			PageContent: "func A() {}",
			Metadata:    map[string]any{"source": "file.go", "identifier": "A"},
		},
		{
			// Same source, different identifier
			PageContent: "func C() {}",
			Metadata:    map[string]any{"source": "file.go", "identifier": "C"},
		},
	}

	context := service.BuildContextForPrompt(docs)

	// Should contain all 3 unique functions
	assert.Contains(t, context, "func A()")
	assert.Contains(t, context, "func B()")
	assert.Contains(t, context, "func C()")

	// All 3 unique docs share the same source file, so they are grouped into
	// one file block (chunk splicing). File: file.go should appear exactly once.
	fileCount := strings.Count(context, "File: file.go")
	assert.Equal(t, 1, fileCount, "Expected docs from the same source to be grouped into 1 file block")
}

// TestBuildContextForPrompt_WithParentText tests that full_parent_text is preferred
func TestBuildContextForPrompt_WithParentText(t *testing.T) {
	service := &builderImpl{cfg: Config{Logger: slog.Default()}}

	docs := []schema.Document{
		{
			PageContent: "chunk of code",
			Metadata:    map[string]any{"source": "file.go", "full_parent_text": "full function with context"},
		},
	}

	context := service.BuildContextForPrompt(docs)

	assert.Contains(t, context, "full function with context")
	assert.NotContains(t, context, "chunk of code")
}

// TestBuildContextForPrompt_WithPackageName tests package name inclusion
func TestBuildContextForPrompt_WithPackageName(t *testing.T) {
	service := &builderImpl{cfg: Config{Logger: slog.Default()}}

	docs := []schema.Document{
		{
			PageContent: "func Process() {}",
			Metadata:    map[string]any{"source": "internal/rag/rag.go", "package_name": "rag"},
		},
	}

	context := service.BuildContextForPrompt(docs)

	assert.Contains(t, context, "Package: rag")
	assert.Contains(t, context, "File: internal/rag/rag.go")
}

// TestBuildContextForPrompt_WithIdentifier tests identifier inclusion
func TestBuildContextForPrompt_WithIdentifier(t *testing.T) {
	service := &builderImpl{cfg: Config{Logger: slog.Default()}}

	docs := []schema.Document{
		{
			PageContent: "type Config struct{}",
			Metadata:    map[string]any{"source": "config.go", "identifier": "Config"},
		},
	}

	context := service.BuildContextForPrompt(docs)

	assert.Contains(t, context, "Identifier: Config")
}

// TestPreFilterBM25_Sorting verifies that preFilterBM25 correctly sorts documents
func TestPreFilterBM25_Sorting(t *testing.T) {
	docs := []schema.Document{
		{PageContent: "package main\n\nfunc main() { helper() }"},
		{PageContent: "func helper() { return }"},
		{PageContent: "package main"},
		{PageContent: "import fmt"},
	}

	query := "helper function"
	result := preFilterBM25(query, docs, 3)

	// Should return top 3
	require.Len(t, result, 3)

	// The doc with "helper" should be ranked highest
	assert.Contains(t, result[0].PageContent, "helper")
}

// TestPreFilterBM25_EmptyQuery tests that empty query returns docs unchanged
func TestPreFilterBM25_EmptyQuery(t *testing.T) {
	docs := []schema.Document{
		{PageContent: "doc1"},
		{PageContent: "doc2"},
	}

	result := preFilterBM25("", docs, 5)

	// Should return original docs
	assert.Equal(t, docs, result)
}

// TestPreFilterBM25_TopKLimits tests that topK properly limits results
func TestPreFilterBM25_TopKLimits(t *testing.T) {
	docs := make([]schema.Document, 20)
	for i := range docs {
		docs[i] = schema.Document{PageContent: strings.Repeat("word ", i+1)}
	}

	result := preFilterBM25("word", docs, 5)

	assert.Len(t, result, 5)
}

// TestGetDocKey tests the document key generation
func TestGetDocKey(t *testing.T) {
	service := &builderImpl{cfg: Config{Logger: slog.Default()}}

	testCases := []struct {
		name     string
		doc      schema.Document
		expected string
		isHash   bool
	}{
		{
			name: "with parent_id",
			doc: schema.Document{
				PageContent: "content",
				Metadata:    map[string]any{"source": "file.go", "parent_id": "parent123"},
			},
			expected: "parent123",
			isHash:   false,
		},
		{
			name: "with source and identifier",
			doc: schema.Document{
				PageContent: "func A()",
				Metadata:    map[string]any{"source": "file.go", "identifier": "A"},
			},
			expected: "file.go-A",
			isHash:   false,
		},
		{
			name: "with source only",
			doc: schema.Document{
				PageContent: "content",
				Metadata:    map[string]any{"source": "file.go"},
			},
			expected: "file.go",
			isHash:   false,
		},
		{
			name: "no metadata - hash",
			doc: schema.Document{
				PageContent: "unique content here",
				Metadata:    map[string]any{},
			},
			expected: "",
			isHash:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := service.getDocKey(tc.doc)
			if tc.isHash {
				assert.NotEmpty(t, key)
				assert.Len(t, key, 64) // SHA256 hex hash is 64 chars
			} else {
				assert.Equal(t, tc.expected, key)
			}
		})
	}
}

// TestGetDocContent tests content retrieval with fallback
func TestGetDocContent(t *testing.T) {
	service := &builderImpl{cfg: Config{Logger: slog.Default()}}

	testCases := []struct {
		name     string
		doc      schema.Document
		expected string
	}{
		{
			name: "with full_parent_text",
			doc: schema.Document{
				PageContent: "chunk",
				Metadata:    map[string]any{"full_parent_text": "full content"},
			},
			expected: "full content",
		},
		{
			name: "without full_parent_text",
			doc: schema.Document{
				PageContent: "page content",
				Metadata:    map[string]any{},
			},
			expected: "page content",
		},
		{
			name: "empty full_parent_text",
			doc: schema.Document{
				PageContent: "page content",
				Metadata:    map[string]any{"full_parent_text": ""},
			},
			expected: "page content",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			content := service.getDocContent(tc.doc)
			assert.Equal(t, tc.expected, content)
		})
	}
}

// TestHashPatch tests the patch hashing function
func TestHashPatch(t *testing.T) {
	service := &builderImpl{cfg: Config{Logger: slog.Default()}}

	patch1 := "+func A() {}"
	patch2 := "+func B() {}"
	patch3 := "+func A() {}" // Same as patch1

	hash1 := service.hashPatch(patch1)
	hash2 := service.hashPatch(patch2)
	hash3 := service.hashPatch(patch3)

	// Same content should produce same hash
	assert.Equal(t, hash1, hash3)

	// Different content should produce different hash
	assert.NotEqual(t, hash1, hash2)

	// Should be 32 hex chars (16 bytes)
	assert.Len(t, hash1, 32)
}

// TestStripPatchNoise_Comprehensive tests the patch cleaning function
func TestStripPatchNoise_Comprehensive(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only metadata lines",
			input:    "--- a/file.go\n+++ b/file.go\n@@ -1,2 +1,3 @@\n",
			expected: "",
		},
		{
			name:     "preserves additions",
			input:    "+func New() {}",
			expected: "+func New() {}",
		},
		{
			name:     "removes deletions",
			input:    "-func Old() {}",
			expected: "",
		},
		{
			name:     "preserves context lines",
			input:    " some context",
			expected: " some context",
		},
		{
			name:     "complex patch",
			input:    "diff --git a/main.go b/main.go\nindex 123..456 789\n--- a/main.go\n+++ b/main.go\n@@ -1,5 +1,5 @@\n package main\n\n-func Old() {}\n+func New() {}\n\n func main() {}",
			expected: " package main\n+func New() {}\n func main() {}",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripPatchNoise(tc.input)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// TestExtractSymbolsFromPatch_Comprehensive tests symbol extraction
func TestExtractSymbolsFromPatch_Comprehensive(t *testing.T) {
	testCases := []struct {
		name           string
		patch          string
		expectedSyms   []string
		unexpectedSyms []string
	}{
		{
			name:           "empty patch",
			patch:          "",
			expectedSyms:   []string{},
			unexpectedSyms: []string{},
		},
		{
			name: "type definitions",
			patch: `+type Config struct {
+    Timeout int
+}
+type Reader interface {
+    Read() error
+}`,
			expectedSyms:   []string{"Config", "Reader"},
			unexpectedSyms: []string{},
		},
		{
			name: "function definitions",
			patch: `+func Process() error { return nil }
+func (c *Config) Method() {}
+func helper() {}`,
			expectedSyms:   []string{"Process", "Method", "helper"},
			unexpectedSyms: []string{},
		},
		{
			name: "type assertions and usage",
			patch: `+val := Config{Timeout: 10}
+cfg := obj.(*Config)
+_ = MyType{}`,
			expectedSyms:   []string{"Config", "MyType"},
			unexpectedSyms: []string{},
		},
		{
			name:           "ignores short names",
			patch:          "+x := 1\n+ab := 2",
			expectedSyms:   []string{},
			unexpectedSyms: []string{"x", "ab"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractSymbolsFromPatch(tc.patch)

			for _, sym := range tc.expectedSyms {
				assert.Contains(t, got, sym, "Expected symbol %s not found", sym)
			}

			for _, sym := range tc.unexpectedSyms {
				assert.NotContains(t, got, sym, "Unexpected symbol %s found", sym)
			}
		})
	}
}

func TestFilterTestDocs(t *testing.T) {
	prodDoc := schema.NewDocument("func Foo() {}", map[string]any{
		"source":  "internal/rag/service.go",
		"is_test": false,
	})
	testDoc := schema.NewDocument("func TestFoo(t *testing.T) {}", map[string]any{
		"source":  "internal/rag/service_test.go",
		"is_test": true,
	})
	noFlagDoc := schema.NewDocument("some content", map[string]any{
		"source": "README.md",
	})

	tests := []struct {
		name      string
		input     []schema.Document
		wantCount int
		wantSrcs  []string
	}{
		{
			name:      "removes test docs",
			input:     []schema.Document{prodDoc, testDoc, noFlagDoc},
			wantCount: 2,
			wantSrcs:  []string{"internal/rag/service.go", "README.md"},
		},
		{
			name:      "all production docs unchanged",
			input:     []schema.Document{prodDoc, noFlagDoc},
			wantCount: 2,
		},
		{
			name:      "all test docs returns empty",
			input:     []schema.Document{testDoc},
			wantCount: 0,
		},
		{
			name:      "empty input",
			input:     []schema.Document{},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterTestDocs(tt.input)
			assert.Len(t, got, tt.wantCount)
			for _, src := range tt.wantSrcs {
				found := false
				for _, doc := range got {
					if doc.Metadata["source"] == src {
						found = true
						break
					}
				}
				assert.True(t, found, "expected source %q in filtered results", src)
			}
		})
	}
}
