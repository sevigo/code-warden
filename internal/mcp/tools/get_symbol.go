package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/storage"
)

// GetSymbol retrieves definition for a symbol by name.
type GetSymbol struct {
	VectorStore storage.ScopedVectorStore
	Logger      *slog.Logger
}

// SymbolDefinition represents a single symbol definition.
type SymbolDefinition struct {
	Content string `json:"content"`
	Source  string `json:"source,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// SymbolResponse is the response for get_symbol tool.
type SymbolResponse struct {
	Name        string             `json:"name"`
	Found       bool               `json:"found"`
	Definitions []SymbolDefinition `json:"definitions,omitempty"`
	Message     string             `json:"message,omitempty"`
}

func (t *GetSymbol) Name() string {
	return "get_symbol"
}

func (t *GetSymbol) Description() string {
	return `Get the definition of a symbol (type, function, interface) by name.
Returns the full definition including signature and documentation.
Use this to understand how a type or function is defined before using it.`
}

func (t *GetSymbol) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The symbol name (e.g., 'ReviewJob', 'GenerateReview', 'Store')",
			},
		},
		"required": []string{"name"},
	}
}

func (t *GetSymbol) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, ok := args["name"].(string)
	if !ok || name == "" {
		return nil, fmt.Errorf("name is required")
	}
	t.Logger.Info("get_symbol: executing tool", "name", name)
	if len(name) > MaxSymbolLength {
		t.Logger.Warn("get_symbol: symbol name too long", "length", len(name))
		return nil, fmt.Errorf("symbol name exceeds maximum length of %d characters", MaxSymbolLength)
	}

	// Search for symbol definition
	query := fmt.Sprintf("definition of %s type function interface struct", name)
	docs, err := t.VectorStore.SimilaritySearch(ctx, query, 5,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "definition",
		}),
	)
	if err != nil {
		t.Logger.Error("failed to get symbol", "name", name, "error", err)
		return nil, fmt.Errorf("failed to get symbol: %w", err)
	}

	if len(docs) == 0 {
		return SymbolResponse{
			Name:    name,
			Found:   false,
			Message: fmt.Sprintf("Symbol '%s' not found", name),
		}, nil
	}

	definitions := make([]SymbolDefinition, 0, len(docs))
	for _, doc := range docs {
		def := SymbolDefinition{
			Content: doc.PageContent,
		}
		if source, ok := doc.Metadata["source"].(string); ok {
			def.Source = source
		}
		if line, ok := doc.Metadata["line"].(int); ok {
			def.Line = line
		}
		definitions = append(definitions, def)
	}

	return SymbolResponse{
		Name:        name,
		Found:       true,
		Definitions: definitions,
	}, nil
}
