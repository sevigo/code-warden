package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/vectorstores"
)

// SearchCodeTool searches for code using semantic similarity.
type SearchCodeTool struct {
	vectorStore storage.ScopedVectorStore
	logger      *slog.Logger
}

func (t *SearchCodeTool) Name() string {
	return "search_code"
}

func (t *SearchCodeTool) Description() string {
	return `Search for code in the repository using semantic similarity.
Returns relevant code snippets that match the query.
Use this to find implementations, understand patterns, or locate related code.`
}

func (t *SearchCodeTool) InputSchema() map[string]any {
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

func (t *SearchCodeTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, fmt.Errorf("query is required")
	}

	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	opts := []vectorstores.Option{}
	if chunkType, ok := args["chunk_type"].(string); ok && chunkType != "" {
		opts = append(opts, vectorstores.WithFilters(map[string]any{
			"chunk_type": chunkType,
		}))
	}

	docsWithScores, err := t.vectorStore.SimilaritySearchWithScores(ctx, query, limit, opts...)
	if err != nil {
		t.logger.Error("search failed", "query", query, "error", err)
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

	return SearchCodeResponse{
		Query:   query,
		Count:   len(results),
		Results: results,
	}, nil
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

// GetArchContextTool retrieves architectural context for a directory.
type GetArchContextTool struct {
	vectorStore storage.ScopedVectorStore
	logger      *slog.Logger
}

func (t *GetArchContextTool) Name() string {
	return "get_arch_context"
}

func (t *GetArchContextTool) Description() string {
	return `Get architectural context for a directory in the repository.
Returns a summary of the directory's purpose, key files, and patterns.
Use this to understand the structure and role of a module before making changes.`
}

func (t *GetArchContextTool) InputSchema() map[string]any {
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

func (t *GetArchContextTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	directory, ok := args["directory"].(string)
	if !ok || directory == "" {
		return nil, fmt.Errorf("directory is required")
	}

	// Search for architectural summary of this directory
	query := fmt.Sprintf("Summary of directory %s architecture structure purpose", directory)
	docs, err := t.vectorStore.SimilaritySearch(ctx, query, 3,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "arch",
		}),
	)
	if err != nil {
		t.logger.Error("failed to get arch context", "directory", directory, "error", err)
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

// ArchContextResponse is the response for get_arch_context tool.
type ArchContextResponse struct {
	Directory string   `json:"directory"`
	Found     bool     `json:"found"`
	Summaries []string `json:"summaries,omitempty"`
	Message   string   `json:"message,omitempty"`
}

// GetSymbolTool retrieves a symbol definition by name.
type GetSymbolTool struct {
	vectorStore storage.ScopedVectorStore
	logger      *slog.Logger
}

func (t *GetSymbolTool) Name() string {
	return "get_symbol"
}

func (t *GetSymbolTool) Description() string {
	return `Get the definition of a symbol (type, function, interface) by name.
Returns the full definition including signature and documentation.
Use this to understand how a type or function is defined before using it.`
}

func (t *GetSymbolTool) InputSchema() map[string]any {
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

func (t *GetSymbolTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, ok := args["name"].(string)
	if !ok || name == "" {
		return nil, fmt.Errorf("name is required")
	}

	// Search for symbol definition
	query := fmt.Sprintf("definition of %s type function interface struct", name)
	docs, err := t.vectorStore.SimilaritySearch(ctx, query, 5,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "definition",
		}),
	)
	if err != nil {
		t.logger.Error("failed to get symbol", "name", name, "error", err)
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

// GetStructureTool returns the overall project structure.
type GetStructureTool struct {
	vectorStore storage.ScopedVectorStore
	projectRoot string
	logger      *slog.Logger
}

func (t *GetStructureTool) Name() string {
	return "get_structure"
}

func (t *GetStructureTool) Description() string {
	return `Get the overall project structure and architecture.
Returns a high-level overview of directories and their purposes.
Use this to understand how the project is organized before making changes.`
}

func (t *GetStructureTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t *GetStructureTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	// Get root-level architectural summary
	query := "project structure architecture overview main directories"
	docs, err := t.vectorStore.SimilaritySearch(ctx, query, 10,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "arch",
		}),
	)
	if err != nil {
		t.logger.Error("failed to get structure", "error", err)
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
		ProjectRoot: t.projectRoot,
		Directories: directories,
	}, nil
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

// ReviewCodeTool performs an internal code review.
type ReviewCodeTool struct {
	ragService rag.Service
	repo       *storage.Repository
	repoConfig *core.RepoConfig
	logger     *slog.Logger
}

func (t *ReviewCodeTool) Name() string {
	return "review_code"
}

func (t *ReviewCodeTool) Description() string {
	return `Perform an internal code review on a diff.
Returns structured feedback with suggestions and verdict.
Use this to validate your changes before creating a PR.`
}

func (t *ReviewCodeTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"diff": map[string]any{
				"type":        "string",
				"description": "The git diff to review",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Optional title for the review context",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional description for additional context",
			},
		},
		"required": []string{"diff"},
	}
}

func (t *ReviewCodeTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	diff, ok := args["diff"].(string)
	if !ok || diff == "" {
		return nil, fmt.Errorf("diff is required")
	}

	// Create a mock event for the review
	title, _ := args["title"].(string)
	if title == "" {
		title = "Internal Code Review"
	}
	description, _ := args["description"].(string)

	event := &core.GitHubEvent{
		PRTitle: title,
		PRBody:  description,
		// These fields are needed but can be mock values for internal review
		RepoFullName:  t.repo.FullName,
		HeadSHA:       "internal-review",
		PRNumber:      0,
		InstallationID: 0,
	}

	// Generate the review
	review, _, err := t.ragService.GenerateReview(ctx, t.repoConfig, t.repo, event, diff, nil)
	if err != nil {
		t.logger.Error("internal review failed", "error", err)
		return nil, fmt.Errorf("review failed: %w", err)
	}

	return ReviewCodeResponse{
		Verdict:     review.Verdict,
		Confidence:  review.Confidence,
		Summary:     review.Summary,
		Suggestions: review.Suggestions,
	}, nil
}

// ReviewCodeResponse is the response for review_code tool.
type ReviewCodeResponse struct {
	Verdict     string            `json:"verdict"`
	Confidence  int               `json:"confidence"`
	Summary     string            `json:"summary"`
	Suggestions []core.Suggestion `json:"suggestions,omitempty"`
}