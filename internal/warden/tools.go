package warden

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/storage"
)

// SearchCodeTool allows agents to search code semantically.
type SearchCodeTool struct {
	searchCode    SearchCodeFunc
	vectorStore   storage.VectorStore
	embedderModel string
	logger        *slog.Logger
}

// NewSearchCodeTool creates a new search code tool.
func NewSearchCodeTool(searchCode SearchCodeFunc, vectorStore storage.VectorStore, embedderModel string, logger *slog.Logger) *SearchCodeTool {
	return &SearchCodeTool{
		searchCode:    searchCode,
		vectorStore:   vectorStore,
		embedderModel: embedderModel,
		logger:        logger,
	}
}

func (t *SearchCodeTool) Name() string {
	return "search_code"
}

func (t *SearchCodeTool) Description() string {
	return "Search the codebase using semantic search. Returns relevant code chunks matching the query."
}

func (t *SearchCodeTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query describing what code to find",
			},
			"collection_name": map[string]any{
				"type":        "string",
				"description": "The vector store collection name for the repository",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return (default 5)",
				"default":     5,
			},
			"chunk_type": map[string]any{
				"type":        "string",
				"description": "Filter by chunk type: code, definition, arch, docs",
				"enum":        []string{"code", "definition", "arch", "docs"},
			},
		},
		"required": []string{"query", "collection_name"},
	}
}

func (t *SearchCodeTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	query, ok := params["query"].(string)
	if !ok {
		return nil, fmt.Errorf("query parameter required and must be string")
	}

	limit := 5
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
	}

	chunkType, _ := params["chunk_type"].(string)

	collectionName, ok := params["collection_name"].(string)
	if !ok {
		return nil, fmt.Errorf("collection_name parameter required")
	}

	t.logger.Debug("search_code tool called",
		"query", query,
		"limit", limit,
		"chunk_type", chunkType)

	// Use callback if available, otherwise use vector store directly
	if t.searchCode != nil {
		return t.searchCode(ctx, collectionName, query, limit, chunkType)
	}

	// Fallback to direct vector store access
	scopedStore := t.vectorStore.ForRepo(collectionName, t.embedderModel)

	var opts []vectorstores.Option
	if chunkType != "" {
		opts = append(opts, vectorstores.WithFilters(map[string]any{
			"chunk_type": chunkType,
		}))
	}

	docs, err := scopedStore.SimilaritySearch(ctx, query, limit, opts...)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	results := make([]map[string]any, len(docs))
	for i, doc := range docs {
		result := map[string]any{
			"content":  doc.PageContent,
			"metadata": doc.Metadata,
		}
		if source, ok := doc.Metadata["source"].(string); ok {
			result["source"] = source
		}
		if line, ok := doc.Metadata["line"].(int); ok {
			result["line"] = line
		}
		results[i] = result
	}

	return map[string]any{
		"query":   query,
		"count":   len(results),
		"results": results,
	}, nil
}

// GetStructureTool allows agents to get project structure.
type GetStructureTool struct {
	getStructure GetStructureFunc
	logger       *slog.Logger
}

// NewGetStructureTool creates a new get structure tool.
func NewGetStructureTool(getStructure GetStructureFunc, logger *slog.Logger) *GetStructureTool {
	return &GetStructureTool{
		getStructure: getStructure,
		logger:       logger,
	}
}

func (t *GetStructureTool) Name() string {
	return "get_structure"
}

func (t *GetStructureTool) Description() string {
	return "Get the project structure showing directories and key files."
}

func (t *GetStructureTool) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"collection_name": map[string]any{
				"type":        "string",
				"description": "The vector store collection name for the repository",
			},
			"root": map[string]any{
				"type":        "string",
				"description": "Root directory to start from (optional, defaults to project root)",
			},
		},
		"required": []string{"collection_name"},
	}
}

func (t *GetStructureTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	t.logger.Debug("get_structure tool called", "params", params)

	root, _ := params["root"].(string)

	collectionName, ok := params["collection_name"].(string)
	if !ok {
		return nil, fmt.Errorf("collection_name parameter required")
	}

	if t.getStructure != nil {
		structure, err := t.getStructure(ctx, collectionName, root)
		if err != nil {
			return nil, fmt.Errorf("failed to get structure: %w", err)
		}
		return map[string]any{
			"root":      root,
			"structure": structure,
		}, nil
	}

	return nil, fmt.Errorf("get_structure callback not configured")
}

// Ensure tools implement the agent.Tool interface
var _ agent.Tool = (*SearchCodeTool)(nil)
var _ agent.Tool = (*GetStructureTool)(nil)
