package warden

import (
	"context"
	"log/slog"

	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/goframe/llms"
)

// SearchCodeFunc is a callback for searching code in the RAG service.
type SearchCodeFunc func(ctx context.Context, collectionName, query string, limit int, chunkType string) ([]map[string]any, error)

// GetStructureFunc is a callback for getting project structure.
type GetStructureFunc func(ctx context.Context, collectionName, root string) (string, error)

// Integration provides a high-level interface for agent-enhanced indexing.
type Integration struct {
	explorer      *Explorer
	vectorStore   storage.VectorStore
	store         storage.Store
	llm           llms.Model
	embedderModel string
	logger        *slog.Logger
}

// IntegrationConfig holds configuration for the warden integration.
type IntegrationConfig struct {
	LLM           llms.Model
	VectorStore   storage.VectorStore
	Store         storage.Store
	EmbedderModel string
	Logger        *slog.Logger
	SearchCode    SearchCodeFunc
	GetStructure  GetStructureFunc
}

// NewIntegration creates a new warden integration.
func NewIntegration(cfg IntegrationConfig) (*Integration, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// Create explorer config with tools that use callbacks
	explorerCfg := ExplorerConfig{
		LLM:           cfg.LLM,
		VectorStore:   cfg.VectorStore,
		Store:         cfg.Store,
		Logger:        cfg.Logger,
		EmbedderModel: cfg.EmbedderModel,
		SearchCode:    cfg.SearchCode,
		GetStructure:  cfg.GetStructure,
	}

	explorer, err := NewExplorer(explorerCfg)
	if err != nil {
		return nil, err
	}

	return &Integration{
		explorer:      explorer,
		vectorStore:   cfg.VectorStore,
		store:         cfg.Store,
		llm:           cfg.LLM,
		embedderModel: cfg.EmbedderModel,
		logger:        cfg.Logger,
	}, nil
}

// RunDesignDocumentGeneration explores the codebase and indexes design documents.
func (i *Integration) RunDesignDocumentGeneration(ctx context.Context, collectionName, repoOwner, repoName, repoPath string) (*DesignDocuments, error) {
	if i.explorer == nil {
		return nil, nil
	}

	docs, err := i.explorer.ExploreCodebase(ctx, collectionName, repoOwner, repoName, repoPath)
	if err != nil {
		i.logger.Error("design document generation failed", "error", err, "repo", repoOwner+"/"+repoName)
		return nil, err
	}

	if err := i.explorer.IndexDesignDocuments(ctx, collectionName, docs); err != nil {
		i.logger.Error("failed to index design documents", "error", err, "repo", repoOwner+"/"+repoName)
		return nil, err
	}

	i.logger.Info("design document generation complete",
		"repo", repoOwner+"/"+repoName,
		"documents", len(docs.Documents))

	return docs, nil
}

// GetExplorer returns the underlying explorer for direct use.
func (i *Integration) GetExplorer() *Explorer {
	return i.explorer
}
