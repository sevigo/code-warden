package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"github.com/sevigo/goframe/vectorstores/qdrant"
)

// VectorStore defines a generic interface for vector database operations.
type VectorStore interface {
	AddDocuments(ctx context.Context, collectionName string, docs []schema.Document) error
	SimilaritySearch(ctx context.Context, collectionName, query string, numDocs int) ([]schema.Document, error)
	DeleteCollection(ctx context.Context, collectionName string) error
	DeleteDocuments(ctx context.Context, collectionName string, documentIDs []string) error
	DeleteDocumentsByFilter(ctx context.Context, collectionName string, filters map[string]any) error
}

// qdrantVectorStore implements VectorStore using Qdrant with client caching.
type qdrantVectorStore struct {
	qdrantHost string
	embedder   embeddings.Embedder
	logger     *slog.Logger

	mu      sync.Mutex
	clients map[string]vectorstores.VectorStore
}

// NewQdrantVectorStore creates a new Qdrant-backed vector store.
func NewQdrantVectorStore(qdrantHost string, embedder embeddings.Embedder, logger *slog.Logger) VectorStore {
	return &qdrantVectorStore{
		qdrantHost: qdrantHost,
		embedder:   embedder,
		logger:     logger,
		clients:    make(map[string]vectorstores.VectorStore),
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

	q.clients[collectionName] = newClient
	return newClient, nil
}

func (q *qdrantVectorStore) AddDocuments(ctx context.Context, collectionName string, docs []schema.Document) error {
	if len(docs) == 0 {
		return nil
	}

	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	_, err = store.AddDocuments(ctx, docs)
	if err != nil {
		return fmt.Errorf("failed to add documents to collection %s: %w", collectionName, err)
	}
	return nil
}

func (q *qdrantVectorStore) SimilaritySearch(ctx context.Context, collectionName, query string, numDocs int) ([]schema.Document, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}
	if numDocs <= 0 {
		return nil, fmt.Errorf("numDocs must be positive, got %d", numDocs)
	}

	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return nil, fmt.Errorf("failed to get store for collection %s: %w", collectionName, err)
	}

	results, err := store.SimilaritySearch(ctx, query, numDocs)
	if err != nil {
		return nil, fmt.Errorf("similarity search failed in collection %s: %w", collectionName, err)
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
