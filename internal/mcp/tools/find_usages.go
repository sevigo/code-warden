package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/storage"
)

// FindUsages finds all usages of a symbol in the codebase.
type FindUsages struct {
	VectorStore storage.ScopedVectorStore
	Logger      *slog.Logger
}

// UsageLocation represents a single usage location.
type UsageLocation struct {
	File    string  `json:"file"`
	Line    int     `json:"line"`
	LineNum int     `json:"line_num"`
	Context string  `json:"context"`
	Score   float64 `json:"score"`
}

// FindUsagesResponse is the response for find_usages tool.
type FindUsagesResponse struct {
	Symbol  string          `json:"symbol"`
	Count   int             `json:"count"`
	Usages  []UsageLocation `json:"usages,omitempty"`
	Message string          `json:"message,omitempty"`
}

func (t *FindUsages) Name() string {
	return "find_usages"
}

func (t *FindUsages) Description() string {
	return `Find all usages of a symbol (function, type, interface) in the codebase.
Returns locations where the symbol is called, referenced, or implemented.
Use this to understand the impact of changes or find all callers of a function.`
}

func (t *FindUsages) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"symbol": map[string]any{
				"type":        "string",
				"description": "The symbol name to search for (e.g., 'ProcessFile', 'Service', 'GenerateReview')",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return (default: 20)",
				"default":     20,
			},
		},
		"required": []string{"symbol"},
	}
}

func (t *FindUsages) Execute(ctx context.Context, args map[string]any) (any, error) {
	symbol, ok := args["symbol"].(string)
	if !ok || symbol == "" {
		return nil, fmt.Errorf("symbol is required")
	}
	t.Logger.Info("find_usages: executing tool", "symbol", symbol)

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
		if limit < 1 {
			limit = 1
		} else if limit > MaxResultLimit {
			limit = MaxResultLimit
		}
	}

	// Build query that emphasizes usage patterns
	// Use multiple query variations to catch different usage contexts
	query := fmt.Sprintf("%s usage call reference implementation type", symbol)

	opts := []vectorstores.Option{
		vectorstores.WithFilters(map[string]any{
			"chunk_type": map[string]any{
				"$ne": "definition", // Exclude definition chunks - we want usage sites
			},
		}),
	}

	docsWithScores, err := t.VectorStore.SimilaritySearchWithScores(ctx, query, limit*2, opts...)
	if err != nil {
		t.Logger.Error("find_usages: search failed", "symbol", symbol, "error", err)
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Filter to usages that actually contain the symbol
	usages := make([]UsageLocation, 0, limit)
	seenFiles := make(map[string]bool)

	for _, doc := range docsWithScores {
		// Check if symbol appears in the content
		if !strings.Contains(doc.Document.PageContent, symbol) {
			continue
		}

		source, _ := doc.Document.Metadata["source"].(string)
		line, _ := doc.Document.Metadata["line"].(int)

		// Deduplicate by file (max one result per file for variety)
		if seenFiles[source] && len(usages) >= limit {
			continue
		}
		seenFiles[source] = true

		// Extract context (lines around the usage)
		context := t.extractContext(doc.Document.PageContent, symbol)

		usages = append(usages, UsageLocation{
			File:    source,
			Line:    line,
			LineNum: line,
			Context: context,
			Score:   float64(doc.Score),
		})

		if len(usages) >= limit {
			break
		}
	}

	if len(usages) == 0 {
		return FindUsagesResponse{
			Symbol:  symbol,
			Count:   0,
			Message: fmt.Sprintf("No usages found for symbol '%s'", symbol),
		}, nil
	}

	return FindUsagesResponse{
		Symbol: symbol,
		Count:  len(usages),
		Usages: usages,
	}, nil
}

// extractContext returns a snippet around the symbol usage.
func (t *FindUsages) extractContext(content, symbol string) string {
	// Find the symbol in content and return surrounding lines
	idx := strings.Index(content, symbol)
	if idx == -1 {
		if len(content) > 200 {
			return content[:200] + "..."
		}
		return content
	}

	// Get 100 chars before and after
	start := max(0, idx-100)
	end := min(len(content), idx+len(symbol)+100)

	context := content[start:end]
	if start > 0 {
		context = "..." + context
	}
	if end < len(content) {
		context += "..."
	}

	return context
}
