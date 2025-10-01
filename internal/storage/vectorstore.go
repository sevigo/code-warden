package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"github.com/sevigo/goframe/vectorstores/qdrant"
)

// VectorStore defines a generic interface for vector database operations.
type VectorStore interface {
	SetBatchConfig(config qdrant.BatchConfig) error

	AddDocuments(ctx context.Context, collectionName string, docs []schema.Document) error
	AddDocumentsBatch(ctx context.Context, collectionName string, docs []schema.Document, progressFn func(processed, total int, duration time.Duration)) error

	SimilaritySearch(ctx context.Context, collectionName, query string, numDocs int) ([]schema.Document, error)
	SimilaritySearchBatch(ctx context.Context, collectionName string, queries []string, numDocs int) ([][]schema.Document, error)

	DeleteCollection(ctx context.Context, collectionName string) error
	DeleteDocuments(ctx context.Context, collectionName string, documentIDs []string) error
	DeleteDocumentsByFilter(ctx context.Context, collectionName string, filters map[string]any) error
}

// qdrantVectorStore implements VectorStore using Qdrant with client caching.
type qdrantVectorStore struct {
	qdrantHost  string
	embedder    embeddings.Embedder
	logger      *slog.Logger
	mu          sync.Mutex
	clients     map[string]vectorstores.VectorStore
	batchConfig *qdrant.BatchConfig
}

// NewQdrantVectorStore creates a new Qdrant-backed vector store.
func NewQdrantVectorStore(qdrantHost string, embedder embeddings.Embedder, batchConfig *qdrant.BatchConfig, logger *slog.Logger) VectorStore {
	return &qdrantVectorStore{
		qdrantHost:  qdrantHost,
		embedder:    embedder,
		logger:      logger,
		clients:     make(map[string]vectorstores.VectorStore),
		batchConfig: batchConfig,
	}
}

// validateCollectionName checks if collection name is valid.
func (q *qdrantVectorStore) validateCollectionName(collectionName string) error {
	if strings.TrimSpace(collectionName) == "" {
		return fmt.Errorf("collection name cannot be empty")
	}
	return nil
}

// getStoreForCollection retrieves or creates a Qdrant client for the specified collection.
func (q *qdrantVectorStore) getStoreForCollection(collectionName string) (vectorstores.VectorStore, error) {
	if err := q.validateCollectionName(collectionName); err != nil {
		return nil, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if client, ok := q.clients[collectionName]; ok {
		return client, nil
	}

	newClient, err := qdrant.New(
		qdrant.WithHost(q.qdrantHost),
		qdrant.WithEmbedder(q.embedder),
		qdrant.WithCollectionName(collectionName),
		qdrant.WithLogger(q.logger),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create qdrant client for collection %s: %w", collectionName, err)
	}

	if q.batchConfig != nil {
		if store, ok := newClient.(*qdrant.Store); ok {
			store.SetBatchConfig(*q.batchConfig)
			q.logger.Info("Applied custom batch configuration to new qdrant client",
				"collection", collectionName,
				"embedding_concurrency", q.batchConfig.EmbeddingMaxConcurrency,
				"embedding_batch_size", q.batchConfig.EmbeddingBatchSize,
			)
		}
	} else {
		q.logger.Info("No custom batch configuration found, using defaults.", "collection", collectionName)
	}

	q.clients[collectionName] = newClient
	return newClient, nil
}

func (q *qdrantVectorStore) AddDocuments(ctx context.Context, collectionName string, docs []schema.Document) error {
	return q.AddDocumentsBatch(ctx, collectionName, docs, nil)
}

func (q *qdrantVectorStore) SetBatchConfig(config qdrant.BatchConfig) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.batchConfig = &config

	for _, client := range q.clients {
		if store, ok := client.(*qdrant.Store); ok {
			store.SetBatchConfig(config)
		}
	}

	return nil
}
func (q *qdrantVectorStore) AddDocumentsBatch(ctx context.Context, collectionName string, docs []schema.Document, progressFn func(processed, total int, duration time.Duration)) error {
	if len(docs) == 0 {
		return nil
	}

	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	qdrantStore, ok := store.(*qdrant.Store)
	if !ok {
		return fmt.Errorf("failed to cast store to *qdrant.Store; cannot use batching feature")
	}

	_, err = qdrantStore.AddDocumentsBatch(ctx, docs, progressFn, vectorstores.WithCollectionName(collectionName))
	if err != nil {
		return fmt.Errorf("failed to add documents to collection %s: %w", collectionName, err)
	}
	return nil
}

func (q *qdrantVectorStore) SimilaritySearch(ctx context.Context, collectionName, query string, numDocs int) ([]schema.Document, error) {
	q.logger.DebugContext(ctx, "Starting similarity search",
		"collection", collectionName,
		"num_docs", numDocs,
		"query_length", len(query),
	)

	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}
	if numDocs <= 0 {
		return nil, fmt.Errorf("numDocs must be positive, got %d", numDocs)
	}

	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		q.logger.ErrorContext(ctx, "Failed to get vector store for collection",
			"collection", collectionName,
			"error", err,
		)
		return nil, fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	q.logger.InfoContext(ctx, "Executing similarity search against Qdrant", "collection", collectionName)
	startTime := time.Now()

	results, err := store.SimilaritySearch(ctx, query, numDocs, vectorstores.WithNameSpace(collectionName))
	if err != nil {
		q.logger.ErrorContext(ctx, "Similarity search execution failed",
			"collection", collectionName,
			"error", err,
			"duration", time.Since(startTime),
		)
		return nil, fmt.Errorf("similarity search failed in collection %s: %w", collectionName, err)
	}

	var sources []string
	for _, doc := range results {
		if source, ok := doc.Metadata["source"].(string); ok {
			sources = append(sources, source)
		}
	}
	q.logger.InfoContext(ctx, "Similarity search completed successfully",
		"collection", collectionName,
		"results_found", len(results),
		"duration", time.Since(startTime),
		"retrieved_sources", sources,
	)

	return results, nil
}

func (q *qdrantVectorStore) SimilaritySearchBatch(ctx context.Context, collectionName string, queries []string, numDocs int) ([][]schema.Document, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	if numDocs <= 0 {
		return nil, fmt.Errorf("numDocs must be positive, got %d", numDocs)
	}

	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return nil, fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	results, err := store.SimilaritySearchBatch(ctx, queries, numDocs, vectorstores.WithNameSpace(collectionName))
	if err != nil {
		return nil, fmt.Errorf("batch similarity search failed in collection %s: %w", collectionName, err)
	}
	return results, nil
}

func (q *qdrantVectorStore) DeleteCollection(ctx context.Context, collectionName string) error {
	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	err = store.DeleteCollection(ctx, collectionName)
	if err != nil {
		return fmt.Errorf("failed to delete collection %s: %w", collectionName, err)
	}

	// Remove from cache after successful deletion to prevent memory leaks
	q.mu.Lock()
	delete(q.clients, collectionName)
	q.mu.Unlock()

	return nil
}

func (q *qdrantVectorStore) DeleteDocuments(ctx context.Context, collectionName string, documentIDs []string) error {
	if len(documentIDs) == 0 {
		return nil
	}

	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	sourcePaths := documentIDs
	filters := map[string]any{
		"source": sourcePaths,
	}

	err = store.DeleteDocumentsByFilter(ctx, filters)
	if err != nil {
		return fmt.Errorf("failed to delete documents from collection %s: %w", collectionName, err)
	}
	return nil
}

func (q *qdrantVectorStore) DeleteDocumentsByFilter(ctx context.Context, collectionName string, filters map[string]any) error {
	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	err = store.DeleteDocumentsByFilter(ctx, filters)
	if err != nil {
		return fmt.Errorf("failed to delete documents by filter from collection %s: %w", collectionName, err)
	}
	return nil
}
