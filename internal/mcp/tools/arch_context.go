package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/storage"
)

// GetArchContext retrieves architectural summaries for a directory.
type GetArchContext struct {
	VectorStore storage.ScopedVectorStore
	Logger      *slog.Logger
}

// ArchContextResponse is the response for get_arch_context tool.
type ArchContextResponse struct {
	Directory string   `json:"directory"`
	Found     bool     `json:"found"`
	Summaries []string `json:"summaries,omitempty"`
	Message   string   `json:"message,omitempty"`
}

func (t *GetArchContext) Name() string {
	return "get_arch_context"
}

func (t *GetArchContext) Description() string {
	return `Get architectural context for a directory in the repository.
Returns a summary of the directory's purpose, key files, and patterns.
Use this to understand the structure and role of a module before making changes.`
}

func (t *GetArchContext) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"directory": map[string]any{
				"type":        "string",
				"description": "The directory path (e.g., 'internal/jobs', 'pkg/cache'). Use '.' for root.",
			},
		},
		"required": []string{"directory"},
	}
}

func (t *GetArchContext) Execute(ctx context.Context, args map[string]any) (any, error) {
	directory, ok := args["directory"].(string)
	if !ok || directory == "" {
		return nil, fmt.Errorf("directory is required")
	}
	t.Logger.Info("get_arch_context: executing tool", "directory", directory)
	if len(directory) > MaxDirPathLength {
		t.Logger.Warn("get_arch_context: directory path too long", "length", len(directory))
		return nil, fmt.Errorf("directory path exceeds maximum length of %d characters", MaxDirPathLength)
	}

	// Search for architectural summary of this directory
	query := fmt.Sprintf("Summary of directory %s architecture structure purpose", directory)
	docs, err := t.VectorStore.SimilaritySearch(ctx, query, 3,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "arch",
		}),
	)
	if err != nil {
		t.Logger.Error("failed to get arch context", "directory", directory, "error", err)
		return nil, fmt.Errorf("failed to get architectural context: %w", err)
	}

	if len(docs) == 0 {
		return ArchContextResponse{
			Directory: directory,
			Found:     false,
			Message:   "No architectural context found for this directory",
		}, nil
	}

	// Combine all found summaries
	summaries := make([]string, 0, len(docs))
	for _, doc := range docs {
		summaries = append(summaries, doc.PageContent)
	}

	return ArchContextResponse{
		Directory: directory,
		Found:     true,
		Summaries: summaries,
	}, nil
}
