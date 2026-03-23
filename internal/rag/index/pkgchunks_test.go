package index

import (
	"testing"

	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
)

func TestBuildPackageChunks(t *testing.T) {
	fileDocs := map[string][]schema.Document{
		"internal/handler/handler.go": {
			{
				PageContent: "package handler",
				Metadata: map[string]any{
					"chunk_type":   "toc",
					"package_name": "handler",
				},
			},
			{
				PageContent: "HandleRequest",
				Metadata: map[string]any{
					"chunk_type": "definition",
					"identifier": "HandleRequest",
					"kind":       "func",
					"keywords":   "handler,request,http",
				},
			},
		},
		"internal/handler/utils.go": {
			{
				PageContent: "package handler",
				Metadata: map[string]any{
					"chunk_type":   "toc",
					"package_name": "handler",
				},
			},
			{
				PageContent: "Helper",
				Metadata: map[string]any{
					"chunk_type": "definition",
					"identifier": "Helper",
					"kind":       "func",
					"keywords":   "helper,utility",
				},
			},
		},
	}

	chunks := BuildPackageChunks(t.Context(), fileDocs, nil)

	assert.NotEmpty(t, chunks, "should generate package chunks")

	foundPkg := false
	for _, chunk := range chunks {
		if chunkType, _ := chunk.Metadata["chunk_type"].(string); chunkType == "package" {
			foundPkg = true
			pkgName, _ := chunk.Metadata["package_name"].(string)
			assert.Equal(t, "handler", pkgName, "package name should be handler")

			source, _ := chunk.Metadata["source"].(string)
			assert.Equal(t, "internal/handler", source, "source should be directory path")

			assert.Contains(t, chunk.PageContent, "# Package: handler")
			assert.Contains(t, chunk.PageContent, "HandleRequest")
			assert.Contains(t, chunk.PageContent, "Helper")
		}
	}
	assert.True(t, foundPkg, "should have created a package chunk")
}

func TestBuildPackageChunksMultipleDirectories(t *testing.T) {
	fileDocs := map[string][]schema.Document{
		"pkg/service/service.go": {
			{
				PageContent: "toc",
				Metadata: map[string]any{
					"chunk_type":   "toc",
					"package_name": "service",
				},
			},
			{
				Metadata: map[string]any{
					"chunk_type": "definition",
					"identifier": "Serve",
					"kind":       "func",
				},
			},
		},
		"pkg/repository/repo.go": {
			{
				Metadata: map[string]any{
					"chunk_type":   "toc",
					"package_name": "repository",
				},
			},
			{
				Metadata: map[string]any{
					"chunk_type": "definition",
					"identifier": "Save",
					"kind":       "func",
				},
			},
		},
	}

	chunks := BuildPackageChunks(t.Context(), fileDocs, nil)

	pkgNames := make(map[string]bool)
	for _, chunk := range chunks {
		if pkgName, _ := chunk.Metadata["package_name"].(string); pkgName != "" {
			pkgNames[pkgName] = true
		}
	}

	assert.True(t, pkgNames["service"], "should have service package")
	assert.True(t, pkgNames["repository"], "should have repository package")
}

func TestBuildCrossFileRelationChunks(t *testing.T) {
	fileDocs := map[string][]schema.Document{
		"handler.go": {
			{
				Metadata: map[string]any{
					"chunk_type": "definition",
					"identifier": "HandleRequest",
					"kind":       "func",
				},
			},
		},
		"main.go": {
			{
				Metadata: map[string]any{
					"chunk_type": "code",
					"symbols":    []string{"HandleRequest", "ServeHTTP"},
				},
			},
		},
	}

	chunks := BuildCrossFileRelationChunks(t.Context(), fileDocs)

	assert.NotEmpty(t, chunks, "should generate relation chunks")

	foundRelation := false
	for _, chunk := range chunks {
		if chunkType, _ := chunk.Metadata["chunk_type"].(string); chunkType == "relations" {
			foundRelation = true
			assert.Contains(t, chunk.PageContent, "main.go")
			assert.Contains(t, chunk.PageContent, "handler.go")
		}
	}
	assert.True(t, foundRelation, "should have created a relation chunk")
}

func TestBuildCrossFileRelationChunksNoRelations(t *testing.T) {
	fileDocs := map[string][]schema.Document{
		"isolated.go": {
			{
				Metadata: map[string]any{
					"chunk_type": "definition",
					"identifier": "Standalone",
					"kind":       "func",
				},
			},
		},
	}

	chunks := BuildCrossFileRelationChunks(t.Context(), fileDocs)
	assert.Empty(t, chunks, "no relations should be generated for isolated file")
}

func TestDedupeAndSortStrings(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "empty input",
			input:    []string{},
			expected: nil,
		},
		{
			name:     "with duplicates",
			input:    []string{"b", "a", "b", "c", "a"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "already sorted unique",
			input:    []string{"a", "b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "with empty strings",
			input:    []string{"a", "", "b", ""},
			expected: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := dedupeAndSortStrings(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
