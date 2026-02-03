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
// It embeds vectorstores.VectorStore to ensure compatibility with GoFrame tools.
//
//go:generate mockgen -destination=../../mocks/mock_vectorstore.go -package=mocks github.com/sevigo/code-warden/internal/storage VectorStore
type VectorStore interface {
	vectorstores.VectorStore
	SetBatchConfig(config qdrant.BatchConfig) error

	// ForRepo returns a scoped store for a specific repository collection and embedder model.
	// The returned ScopedVectorStore implements vectorstores.VectorStore directly,
	// so it can be passed to goframe tools that expect that interface.
	ForRepo(collectionName, embedderModel string) ScopedVectorStore

	// Collection-specific methods (legacy, prefer ForRepo() for new code)
	AddDocumentsToCollection(ctx context.Context, collectionName, embedderModelName string, docs []schema.Document, progressFn func(processed, total int, duration time.Duration)) error
	SearchCollection(ctx context.Context, collectionName, embedderModelName, query string, numDocs int) ([]schema.Document, error)
	SearchCollectionBatch(ctx context.Context, collectionName, embedderModelName string, queries []string, numDocs int) ([][]schema.Document, error)
	DeleteCollection(ctx context.Context, collectionName string) error
	DeleteDocumentsFromCollection(ctx context.Context, collectionName, embedderModelName string, documentIDs []string) error
	DeleteDocumentsFromCollectionByFilter(ctx context.Context, collectionName, embedderModelName string, filters map[string]any) error
}

// ScopedVectorStore is a VectorStore scoped to a specific collection and embedder model.
// It implements vectorstores.VectorStore directly without requiring collection/embedder names.
type ScopedVectorStore interface {
	vectorstores.VectorStore
	CollectionName() string
	EmbedderModel() string
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
		qdrantHost:  cfg.Storage.QdrantHost,
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
		ollama.WithServerURL(q.cfg.AI.OllamaHost),
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

	opts := []qdrant.Option{
		qdrant.WithHost(q.qdrantHost),
		qdrant.WithEmbedder(embedder),
		qdrant.WithCollectionName(collectionName),
		qdrant.WithLogger(q.logger),
	}

	if q.cfg.Features.EnableBinaryQuantization {
		opts = append(opts, qdrant.WithBinaryQuantization(true))
	}

	newClient, err := qdrant.New(opts...)
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

func (q *qdrantVectorStore) AddDocumentsToCollection(ctx context.Context, collectionName, embedderModelName string, docs []schema.Document, progressFn func(processed, total int, duration time.Duration)) error {
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

// SearchCollection is the renamed SimilaritySearch
func (q *qdrantVectorStore) SearchCollection(ctx context.Context, collectionName, embedderModelName, query string, numDocs int) ([]schema.Document, error) {
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
	// Use goframe's SimilaritySearch
	results, err := store.SimilaritySearch(ctx, query, numDocs, vectorstores.WithCollectionName(collectionName))
	if err != nil {
		q.logger.ErrorContext(ctx, "Similarity search execution failed", "collection", collectionName, "error", err)
		return nil, fmt.Errorf("similarity search failed: %w", err)
	}

	q.logger.InfoContext(ctx, "Similarity search completed successfully", "results_found", len(results), "duration", time.Since(startTime))
	return results, nil
}

func (q *qdrantVectorStore) SearchCollectionBatch(ctx context.Context, collectionName, embedderModelName string, queries []string, numDocs int) ([][]schema.Document, error) {
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

func (q *qdrantVectorStore) DeleteDocumentsFromCollection(ctx context.Context, collectionName, embedderModelName string, documentIDs []string) error {
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

func (q *qdrantVectorStore) DeleteDocumentsFromCollectionByFilter(ctx context.Context, collectionName, embedderModelName string, filters map[string]any) error {
	store, err := q.getStoreForCollection(collectionName, embedderModelName)
	if err != nil {
		return err
	}
	return store.DeleteDocumentsByFilter(ctx, filters)
}

func (q *qdrantVectorStore) ListCollections(_ context.Context) ([]string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cols := make([]string, 0, len(q.clients))
	for name := range q.clients {
		cols = append(cols, name)
	}
	return cols, nil
}

func (q *qdrantVectorStore) validateCollectionName(collectionName string) error {
	if strings.TrimSpace(collectionName) == "" {
		return fmt.Errorf("collection name cannot be empty")
	}
	return nil
}

// Implement vectorstores.VectorStore methods

// extractCollectionName uses goframe's ParseOptions to extract collection name from options.
func extractCollectionName(opts ...vectorstores.Option) string {
	parsed := vectorstores.ParseOptions(opts...)
	return parsed.CollectionName
}

func (q *qdrantVectorStore) AddDocuments(ctx context.Context, docs []schema.Document, opts ...vectorstores.Option) ([]string, error) {
	collectionName := extractCollectionName(opts...)
	if collectionName == "" {
		return nil, fmt.Errorf("collection name required via WithCollectionName option for AddDocuments")
	}

	// Use default embedder from config
	embedderModel := q.cfg.AI.EmbedderModel

	q.logger.Debug("AddDocuments via generic interface", "collection", collectionName, "embedder", embedderModel, "docs", len(docs))

	store, err := q.getStoreForCollection(collectionName, embedderModel)
	if err != nil {
		return nil, err
	}

	return store.AddDocuments(ctx, docs, opts...)
}

func (q *qdrantVectorStore) SimilaritySearch(ctx context.Context, query string, numDocs int, opts ...vectorstores.Option) ([]schema.Document, error) {
	collectionName := extractCollectionName(opts...)
	if collectionName == "" {
		return nil, fmt.Errorf("collection name required via WithCollectionName option for SimilaritySearch")
	}

	// Use default embedder from config
	embedderModel := q.cfg.AI.EmbedderModel

	q.logger.Debug("SimilaritySearch via generic interface", "collection", collectionName, "embedder", embedderModel)

	store, err := q.getStoreForCollection(collectionName, embedderModel)
	if err != nil {
		return nil, err
	}

	return store.SimilaritySearch(ctx, query, numDocs, opts...)
}

func (q *qdrantVectorStore) SimilaritySearchWithScores(ctx context.Context, query string, numDocs int, opts ...vectorstores.Option) ([]vectorstores.DocumentWithScore, error) {
	collectionName := extractCollectionName(opts...)
	if collectionName == "" {
		return nil, fmt.Errorf("collection name required via WithCollectionName option")
	}

	embedderModel := q.cfg.AI.EmbedderModel
	store, err := q.getStoreForCollection(collectionName, embedderModel)
	if err != nil {
		return nil, err
	}

	return store.SimilaritySearchWithScores(ctx, query, numDocs, opts...)
}

func (q *qdrantVectorStore) DeleteDocumentsByFilter(ctx context.Context, filters map[string]any, opts ...vectorstores.Option) error {
	collectionName := extractCollectionName(opts...)
	if collectionName == "" {
		return fmt.Errorf("collection name required via WithCollectionName option")
	}

	embedderModel := q.cfg.AI.EmbedderModel
	store, err := q.getStoreForCollection(collectionName, embedderModel)
	if err != nil {
		return err
	}

	return store.DeleteDocumentsByFilter(ctx, filters, opts...)
}

func (q *qdrantVectorStore) SimilaritySearchBatch(ctx context.Context, queries []string, numDocs int, opts ...vectorstores.Option) ([][]schema.Document, error) {
	collectionName := extractCollectionName(opts...)
	if collectionName == "" {
		return nil, fmt.Errorf("collection name required via WithCollectionName option")
	}

	embedderModel := q.cfg.AI.EmbedderModel
	store, err := q.getStoreForCollection(collectionName, embedderModel)
	if err != nil {
		return nil, err
	}

	return store.SimilaritySearchBatch(ctx, queries, numDocs, opts...)
}

// ForRepo returns a scoped store for a specific repository collection and embedder model.
func (q *qdrantVectorStore) ForRepo(collectionName, embedderModel string) ScopedVectorStore {
	return &scopedVectorStore{
		parent:         q,
		collectionName: collectionName,
		embedderModel:  embedderModel,
	}
}

// scopedVectorStore wraps qdrantVectorStore with pre-configured collection and embedder.
type scopedVectorStore struct {
	parent         *qdrantVectorStore
	collectionName string
	embedderModel  string
}

// Ensure scopedVectorStore implements ScopedVectorStore
var _ ScopedVectorStore = (*scopedVectorStore)(nil)

// CollectionName returns the scoped collection name.
func (s *scopedVectorStore) CollectionName() string {
	return s.collectionName
}

// EmbedderModel returns the scoped embedder model name.
func (s *scopedVectorStore) EmbedderModel() string {
	return s.embedderModel
}

// AddDocuments delegates to the parent's AddDocumentsToCollection.
func (s *scopedVectorStore) AddDocuments(ctx context.Context, docs []schema.Document, _ ...vectorstores.Option) ([]string, error) {
	err := s.parent.AddDocumentsToCollection(ctx, s.collectionName, s.embedderModel, docs, nil)
	if err != nil {
		return nil, err
	}
	// Return IDs (we don't have them from AddDocumentsToCollection, so return empty for now)
	ids := make([]string, len(docs))
	for i, doc := range docs {
		if id, ok := doc.Metadata["id"].(string); ok {
			ids[i] = id
		}
	}
	return ids, nil
}

// SimilaritySearch delegates to the parent's SearchCollection.
func (s *scopedVectorStore) SimilaritySearch(ctx context.Context, query string, numDocs int, _ ...vectorstores.Option) ([]schema.Document, error) {
	return s.parent.SearchCollection(ctx, s.collectionName, s.embedderModel, query, numDocs)
}

// SimilaritySearchWithScores delegates to the underlying store.
func (s *scopedVectorStore) SimilaritySearchWithScores(ctx context.Context, query string, numDocs int, opts ...vectorstores.Option) ([]vectorstores.DocumentWithScore, error) {
	store, err := s.parent.getStoreForCollection(s.collectionName, s.embedderModel)
	if err != nil {
		return nil, err
	}
	return store.SimilaritySearchWithScores(ctx, query, numDocs, opts...)
}

// SimilaritySearchBatch delegates to the parent's SearchCollectionBatch.
func (s *scopedVectorStore) SimilaritySearchBatch(ctx context.Context, queries []string, numDocs int, _ ...vectorstores.Option) ([][]schema.Document, error) {
	return s.parent.SearchCollectionBatch(ctx, s.collectionName, s.embedderModel, queries, numDocs)
}

// DeleteDocumentsByFilter delegates to the parent's DeleteDocumentsFromCollectionByFilter.
func (s *scopedVectorStore) DeleteDocumentsByFilter(ctx context.Context, filters map[string]any, _ ...vectorstores.Option) error {
	return s.parent.DeleteDocumentsFromCollectionByFilter(ctx, s.collectionName, s.embedderModel, filters)
}

// DeleteCollection deletes the scoped collection (ignores collectionName arg since already scoped).
func (s *scopedVectorStore) DeleteCollection(ctx context.Context, _ string) error {
	return s.parent.DeleteCollection(ctx, s.collectionName)
}

// ListCollections returns just this scoped collection.
func (s *scopedVectorStore) ListCollections(_ context.Context) ([]string, error) {
	return []string{s.collectionName}, nil
}
