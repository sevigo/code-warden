package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/storage"
)

// SearchCode performs a semantic code search.
type SearchCode struct {
	VectorStore storage.ScopedVectorStore
	Logger      *slog.Logger
}

// CodeResult represents a single search result.
type CodeResult struct {
	Content  string         `json:"content"`
	Score    float64        `json:"score"`
	Metadata map[string]any `json:"metadata"`
}

// SearchCodeResponse is the response for search_code tool.
type SearchCodeResponse struct {
	Query   string       `json:"query"`
	Count   int          `json:"count"`
	Results []CodeResult `json:"results"`
}

func (t *SearchCode) Name() string {
	return "search_code"
}

func (t *SearchCode) Description() string {
	return `Search for code in the repository using semantic similarity.
Returns relevant code snippets that match the query.
Use this to find implementations, understand patterns, or locate related code.`
}

func (t *SearchCode) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query describing what code you're looking for",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return (default: 10)",
				"default":     10,
			},
			"chunk_type": map[string]any{
				"type":        "string",
				"description": "Filter by chunk type: 'code', 'arch', 'definition'",
			},
		},
		"required": []string{"query"},
	}
}

func (t *SearchCode) Execute(ctx context.Context, args map[string]any) (any, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		t.Logger.Warn("search_code: missing query parameter")
		return nil, fmt.Errorf("query is required")
	}
	if len(query) > MaxQueryLength {
		t.Logger.Warn("search_code: query too long", "length", len(query))
		return nil, fmt.Errorf("query exceeds maximum length of %d characters", MaxQueryLength)
	}

	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
		if limit < MinResultLimit {
			limit = MinResultLimit
		} else if limit > MaxResultLimit {
			limit = MaxResultLimit
		}
	}

	opts := []vectorstores.Option{}
	chunkType := ""
	if ct, ok := args["chunk_type"].(string); ok && ct != "" {
		chunkType = ct
		opts = append(opts, vectorstores.WithFilters(map[string]any{
			"chunk_type": ct,
		}))
	}

	t.Logger.Info("search_code: executing search",
		"query", query,
		"limit", limit,
		"chunk_type", chunkType)

	docsWithScores, err := t.VectorStore.SimilaritySearchWithScores(ctx, query, limit, opts...)
	if err != nil {
		t.Logger.Error("search_code: search failed",
			"query", query,
			"error", err)
		return nil, fmt.Errorf("search failed: %w", err)
	}

	results := make([]CodeResult, 0, len(docsWithScores))
	for _, ds := range docsWithScores {
		result := CodeResult{
			Content:  ds.Document.PageContent,
			Score:    float64(ds.Score),
			Metadata: ds.Document.Metadata,
		}
		results = append(results, result)
	}

	t.Logger.Info("search_code: search completed",
		"query", query,
		"results_count", len(results),
		"top_score", func() float64 {
			if len(results) > 0 {
				return results[0].Score
			}
			return 0
		}())

	return SearchCodeResponse{
		Query:   query,
		Count:   len(results),
		Results: results,
	}, nil
}
