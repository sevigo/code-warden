package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/storage"
)

// GetStructure retrieves overall project structure.
type GetStructure struct {
	VectorStore storage.ScopedVectorStore
	ProjectRoot string
	Logger      *slog.Logger
}

// DirectoryInfo represents information about a directory.
type DirectoryInfo struct {
	Path      string `json:"path"`
	Summary   string `json:"summary"`
	Language  string `json:"language,omitempty"`
	ChunkType string `json:"chunkType"`
}

// StructureResponse is the response for get_structure tool.
type StructureResponse struct {
	ProjectRoot string          `json:"projectRoot"`
	Directories []DirectoryInfo `json:"directories"`
}

func (t *GetStructure) Name() string {
	return "get_structure"
}

func (t *GetStructure) Description() string {
	return `Get the overall project structure and architecture.
Returns a high-level overview of directories and their purposes.
Use this to understand how the project is organized before making changes.`
}

func (t *GetStructure) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *GetStructure) Execute(ctx context.Context, _ map[string]any) (any, error) {
	projectRoot := ProjectRootFromContext(ctx)
	if projectRoot == "" {
		projectRoot = t.ProjectRoot
	}
	t.Logger.Info("get_structure: executing tool", "project_root", projectRoot)

	// Get root-level architectural summary
	query := "project structure architecture overview main directories"
	docs, err := t.VectorStore.SimilaritySearch(ctx, query, 10,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "arch",
		}),
	)
	if err != nil {
		t.Logger.Error("failed to get structure", "error", err)
		return nil, fmt.Errorf("failed to get project structure: %w", err)
	}

	directories := make([]DirectoryInfo, 0)
	seen := make(map[string]bool)

	for _, doc := range docs {
		dir, ok := doc.Metadata["source"].(string)
		if !ok || dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true

		info := DirectoryInfo{
			Path:      dir,
			Summary:   doc.PageContent,
			ChunkType: "arch",
		}
		if lang, ok := doc.Metadata["language"].(string); ok {
			info.Language = lang
		}
		directories = append(directories, info)
	}

	return StructureResponse{
		ProjectRoot: projectRoot,
		Directories: directories,
	}, nil
}
