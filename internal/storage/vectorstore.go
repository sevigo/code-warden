package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"github.com/sevigo/goframe/vectorstores/qdrant"
)

// VectorStore interface updated for multi-model support
type VectorStore interface {
	SetBatchConfig(config qdrant.BatchConfig) error
	AddDocumentsBatch(ctx context.Context, collectionName, embedderModelName string, docs []schema.Document, progressFn func(processed, total int, duration time.Duration)) error
	SimilaritySearch(ctx context.Context, collectionName, embedderModelName, query string, numDocs int) ([]schema.Document, error)
	SimilaritySearchBatch(ctx context.Context, collectionName, embedderModelName string, queries []string, numDocs int) ([][]schema.Document, error)
	DeleteCollection(ctx context.Context, collectionName string) error
	DeleteDocuments(ctx context.Context, collectionName, embedderModelName string, documentIDs []string) error
	DeleteDocumentsByFilter(ctx context.Context, collectionName, embedderModelName string, filters map[string]any) error
}

// Ensure qdrantVectorStore implements VectorStore
var _ VectorStore = (*qdrantVectorStore)(nil)

// qdrantVectorStore implements VectorStore using Qdrant with client caching.
type qdrantVectorStore struct {
	qdrantHost  string
	logger      *slog.Logger
	mu          sync.Mutex   // for clients
	embedderMu  sync.RWMutex // for embedders
	clients     map[string]vectorstores.VectorStore
	embedders   map[string]embeddings.Embedder
	batchConfig *qdrant.BatchConfig
	cfg         *config.Config
}

// QdrantStoreOption defines a functional option for configuring the Qdrant vector store.
type QdrantStoreOption func(*qdrantVectorStore)

// WithBatchConfig sets the batch processing configuration for the vector store.
func WithBatchConfig(config *qdrant.BatchConfig) QdrantStoreOption {
	return func(s *qdrantVectorStore) {
		s.batchConfig = config
	}
}

// WithInitialEmbedder pre-populates the vector store with a configured embedder.
func WithInitialEmbedder(modelName string, embedder embeddings.Embedder) QdrantStoreOption {
	return func(s *qdrantVectorStore) {
		s.logger.Info("Pre-registering initial embedder", "model", modelName)
		s.embedders[modelName] = embedder
	}
}

// NewQdrantVectorStore creates a new Qdrant-backed vector store.
func NewQdrantVectorStore(cfg *config.Config, logger *slog.Logger, opts ...QdrantStoreOption) VectorStore {
	defaultConfig := &qdrant.BatchConfig{
		BatchSize:               256,
		MaxConcurrency:          4,
		EmbeddingBatchSize:      90,
		EmbeddingMaxConcurrency: 1,
		RetryAttempts:           qdrant.DefaultRetryAttempts,
		RetryDelay:              qdrant.DefaultRetryDelay,
		RetryJitter:             qdrant.DefaultRetryJitter,
		MaxRetryDelay:           qdrant.DefaultMaxRetryDelay,
	}
	s := &qdrantVectorStore{
		qdrantHost:  cfg.QdrantHost,
		logger:      logger,
		clients:     make(map[string]vectorstores.VectorStore),
		embedders:   make(map[string]embeddings.Embedder),
		batchConfig: defaultConfig,
		cfg:         cfg,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// getOrCreateEmbedder creates and caches embedder clients on the fly.
func (q *qdrantVectorStore) getOrCreateEmbedder(modelName string) (embeddings.Embedder, error) {
	// Try read lock first (non-blocking for concurrent reads)
	q.embedderMu.RLock()
	if embedder, exists := q.embedders[modelName]; exists {
		q.embedderMu.RUnlock()
		return embedder, nil
	}
	q.embedderMu.RUnlock()

	// Not found, acquire write lock
	q.embedderMu.Lock()
	defer q.embedderMu.Unlock()

	// Double-check pattern - another goroutine might have created it
	if embedder, exists := q.embedders[modelName]; exists {
		return embedder, nil
	}

	q.logger.Info("Creating and caching new embedder client", "model", modelName)

	// Currently only Ollama is supported; can be extended later.
	baseEmbedder, err := ollama.New(
		ollama.WithServerURL(q.cfg.OllamaHost),
		ollama.WithModel(modelName),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create new ollama instance for %s: %w", modelName, err)
	}

	wrappedEmbedder, err := embeddings.NewEmbedder(baseEmbedder)
	if err != nil {
		return nil, fmt.Errorf("failed to wrap embedder for %s: %w", modelName, err)
	}

	q.embedders[modelName] = wrappedEmbedder
	return wrappedEmbedder, nil
}

// getStoreForCollection retrieves or creates a Qdrant client for the specified collection.
func (q *qdrantVectorStore) getStoreForCollection(collectionName string, embedderModelName string) (vectorstores.VectorStore, error) {
	if err := q.validateCollectionName(collectionName); err != nil {
		return nil, err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// If a client for this collection already exists, return it.
	if client, ok := q.clients[collectionName]; ok {
		return client, nil
	}

	embedder, err := q.getOrCreateEmbedder(embedderModelName)
	if err != nil {
		return nil, fmt.Errorf("cannot create store without a valid embedder for model %s: %w", embedderModelName, err)
	}

	newClient, err := qdrant.New(
		qdrant.WithHost(q.qdrantHost),
		qdrant.WithEmbedder(embedder),
		qdrant.WithCollectionName(collectionName),
		qdrant.WithLogger(q.logger),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create qdrant client for collection %s: %w", collectionName, err)
	}

	if q.batchConfig != nil {
		if store, ok := newClient.(*qdrant.Store); ok {
			store.SetBatchConfig(*q.batchConfig)
			q.logger.Info("Applied custom batch configuration to new qdrant client", "collection", collectionName)
		}
	}

	q.clients[collectionName] = newClient
	return newClient, nil
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

func (q *qdrantVectorStore) AddDocumentsBatch(ctx context.Context, collectionName, embedderModelName string, docs []schema.Document, progressFn func(processed, total int, duration time.Duration)) error {
	if len(docs) == 0 {
		return nil
	}

	store, err := q.getStoreForCollection(collectionName, embedderModelName)
	if err != nil {
		return fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	qdrantStore, ok := store.(*qdrant.Store)
	if !ok {
		return fmt.Errorf("failed to cast store to *qdrant.Store; cannot use batching feature")
	}

	_, err = qdrantStore.AddDocumentsBatch(ctx, docs, progressFn, vectorstores.WithCollectionName(collectionName))
	return err
}

func (q *qdrantVectorStore) SimilaritySearch(ctx context.Context, collectionName, embedderModelName, query string, numDocs int) ([]schema.Document, error) {
	q.logger.DebugContext(ctx, "Starting similarity search", "collection", collectionName, "embedder", embedderModelName)

	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}
	if numDocs <= 0 {
		return nil, fmt.Errorf("numDocs must be positive, got %d", numDocs)
	}

	store, err := q.getStoreForCollection(collectionName, embedderModelName)
	if err != nil {
		q.logger.Error("Can't get vector store",
			"error", err,
			"collectionName", collectionName,
			"embedderModelName", embedderModelName,
		)
		return nil, err
	}

	startTime := time.Now()
	results, err := store.SimilaritySearch(ctx, query, numDocs, vectorstores.WithCollectionName(collectionName))
	if err != nil {
		q.logger.ErrorContext(ctx, "Similarity search execution failed", "collection", collectionName, "error", err)
		return nil, fmt.Errorf("similarity search failed: %w", err)
	}

	q.logger.InfoContext(ctx, "Similarity search completed successfully", "results_found", len(results), "duration", time.Since(startTime))
	return results, nil
}

func (q *qdrantVectorStore) SimilaritySearchBatch(ctx context.Context, collectionName, embedderModelName string, queries []string, numDocs int) ([][]schema.Document, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	if numDocs <= 0 {
		return nil, fmt.Errorf("numDocs must be positive, got %d", numDocs)
	}

	store, err := q.getStoreForCollection(collectionName, embedderModelName)
	if err != nil {
		return nil, err
	}

	return store.SimilaritySearchBatch(ctx, queries, numDocs, vectorstores.WithCollectionName(collectionName))
}

func (q *qdrantVectorStore) DeleteCollection(ctx context.Context, collectionName string) error {
	q.mu.Lock()
	client, ok := q.clients[collectionName]
	delete(q.clients, collectionName)
	q.mu.Unlock()

	if !ok {
		return fmt.Errorf("no active client for collection %s, cannot delete", collectionName)
	}

	if err := client.DeleteCollection(ctx, collectionName); err != nil {
		return fmt.Errorf("failed to delete collection %s: %w", collectionName, err)
	}

	return nil
}

func (q *qdrantVectorStore) DeleteDocuments(ctx context.Context, collectionName, embedderModelName string, documentIDs []string) error {
	if len(documentIDs) == 0 {
		return nil
	}

	store, err := q.getStoreForCollection(collectionName, embedderModelName)
	if err != nil {
		return err
	}

	filters := map[string]any{"source": documentIDs}
	return store.DeleteDocumentsByFilter(ctx, filters)
}

func (q *qdrantVectorStore) DeleteDocumentsByFilter(ctx context.Context, collectionName, embedderModelName string, filters map[string]any) error {
	store, err := q.getStoreForCollection(collectionName, embedderModelName)
	if err != nil {
		return err
	}
	return store.DeleteDocumentsByFilter(ctx, filters)
}

func (q *qdrantVectorStore) validateCollectionName(collectionName string) error {
	if strings.TrimSpace(collectionName) == "" {
		return fmt.Errorf("collection name cannot be empty")
	}
	return nil
}
