// Package storage provides an abstraction for vector database interactions.
package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sevigo/goframe/embeddings"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"github.com/sevigo/goframe/vectorstores/qdrant"
)

// VectorStore defines the contract for interacting with vector databases.
type VectorStore interface {
	// AddDocuments embeds and stores documents into a collection.
	AddDocuments(ctx context.Context, collectionName string, docs []schema.Document) error

	// SimilaritySearch finds the most relevant documents based on a query.
	SimilaritySearch(ctx context.Context, collectionName, query string, numDocs int) ([]schema.Document, error)

	// DeleteCollection removes a collection and all its data.
	DeleteCollection(ctx context.Context, collectionName string) error
}

// qdrantVectorStore implements VectorStore using Qdrant as the backend.
type qdrantVectorStore struct {
	qdrantHost string
	embedder   embeddings.Embedder
	logger     *slog.Logger
}

// NewQdrantVectorStore creates a new Qdrant-backed vector store.
func NewQdrantVectorStore(qdrantHost string, embedder embeddings.Embedder, logger *slog.Logger) VectorStore {
	return &qdrantVectorStore{
		qdrantHost: qdrantHost,
		embedder:   embedder,
		logger:     logger,
	}
}

// getStoreForCollection creates a Qdrant client for the specified collection.
func (q *qdrantVectorStore) getStoreForCollection(collectionName string) (vectorstores.VectorStore, error) {
	if strings.TrimSpace(collectionName) == "" {
		return nil, fmt.Errorf("collection name cannot be empty")
	}
	return qdrant.New(
		qdrant.WithHost(q.qdrantHost),
		qdrant.WithEmbedder(q.embedder),
		qdrant.WithCollectionName(collectionName),
		qdrant.WithLogger(q.logger),
	)
}

// AddDocuments adds documents to the specified collection.
func (q *qdrantVectorStore) AddDocuments(ctx context.Context, collectionName string, docs []schema.Document) error {
	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return fmt.Errorf("failed to get qdrant store for collection %s: %w", collectionName, err)
	}

	_, err = store.AddDocuments(ctx, docs)
	if err != nil {
		return fmt.Errorf("failed to add documents to qdrant collection %s: %w", collectionName, err)
	}
	return nil
}

// SimilaritySearch performs vector similarity search in the specified collection.
func (q *qdrantVectorStore) SimilaritySearch(ctx context.Context, collectionName, query string, numDocs int) ([]schema.Document, error) {
	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return nil, fmt.Errorf("failed to get qdrant store for collection %s: %w", collectionName, err)
	}

	return store.SimilaritySearch(ctx, query, numDocs)
}

// DeleteCollection removes the specified collection and all its data.
func (q *qdrantVectorStore) DeleteCollection(ctx context.Context, collectionName string) error {
	store, err := q.getStoreForCollection(collectionName)
	if err != nil {
		return fmt.Errorf("failed to get qdrant store for collection %s: %w", collectionName, err)
	}

	return store.DeleteCollection(ctx, collectionName)
}
