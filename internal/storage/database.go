package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
	// The blank import is required to register the PostgreSQL driver.
	_ "github.com/lib/pq"

	"github.com/sevigo/code-warden/internal/core"
)

// Repository represents a stored Git repository.
type Repository struct {
	ID                   int64     `json:"id" db:"id"`
	RepoID               int64     `json:"repo_id" db:"repo_id"`
	FullName             string    `json:"full_name" db:"full_name"`
	ClonePath            string    `json:"clone_path" db:"clone_path"`
	QdrantCollectionName string    `json:"qdrant_collection_name" db:"qdrant_collection_name"`
	LastIndexedSHA       string    `json:"last_indexed_sha" db:"last_indexed_sha"`
	EmbedderModelName    string    `json:"embedder_model_name" db:"embedder_model_name"`
	LastReviewDate       time.Time `json:"last_review_date" db:"last_review_date"`
	CreatedAt            time.Time `json:"created_at" db:"created_at"`
	UpdatedAt            time.Time `json:"updated_at" db:"updated_at"`
}

// Store defines the interface for all database operations.
type Store interface {
	SaveReview(ctx context.Context, review *core.Review) error
	GetLatestReviewForPR(ctx context.Context, repoFullName string, prNumber int) (*core.Review, error)
	GetAllReviewsForPR(ctx context.Context, repoFullName string, prNumber int) ([]*core.Review, error)
	CreateRepository(ctx context.Context, repo *Repository) error
	GetRepositoryByFullName(ctx context.Context, fullName string) (*Repository, error)
	GetRepositoryByClonePath(ctx context.Context, clonePath string) (*Repository, error)
	UpdateRepository(ctx context.Context, repo *Repository) error
	GetAllRepositories(ctx context.Context) ([]*Repository, error)
}

type postgresStore struct {
	db *sqlx.DB
}

// NewStore creates a new Store
func NewStore(db *sqlx.DB) Store {
	return &postgresStore{db: db}
}

// SaveReview inserts a new review record into the database.
func (s *postgresStore) SaveReview(ctx context.Context, review *core.Review) error {
	query := `
		INSERT INTO reviews (repo_full_name, pr_number, head_sha, review_content) 
		VALUES ($1, $2, $3, $4)`
	_, err := s.db.ExecContext(ctx, query, review.RepoFullName, review.PRNumber, review.HeadSHA, review.ReviewContent)
	return err
}

// GetLatestReviewForPR retrieves the most recent review for a given pull request.
func (s *postgresStore) GetLatestReviewForPR(ctx context.Context, repoFullName string, prNumber int) (*core.Review, error) {
	query := `
		SELECT id, repo_full_name, pr_number, head_sha, review_content, created_at 
		FROM reviews 
		WHERE repo_full_name = $1 AND pr_number = $2 
		ORDER BY created_at DESC 
		LIMIT 1`

	row := s.db.QueryRowContext(ctx, query, repoFullName, prNumber)

	var r core.Review
	err := row.Scan(&r.ID, &r.RepoFullName, &r.PRNumber, &r.HeadSHA, &r.ReviewContent, &r.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no previous review found for PR %s#%d", repoFullName, prNumber)
		}
		return nil, err
	}
	return &r, nil
}

// CreateRepository inserts a new repository record into the database.
func (s *postgresStore) CreateRepository(ctx context.Context, repo *Repository) error {
	query := `
		INSERT INTO repositories (full_name, clone_path, qdrant_collection_name, embedder_model_name, last_indexed_sha) 
		VALUES (:full_name, :clone_path, :qdrant_collection_name, :embedder_model_name, :last_indexed_sha) 
		RETURNING id, created_at, updated_at`
	stmt, err := s.db.PrepareNamedContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to prepare statement for creating repository: %w", err)
	}
	defer stmt.Close()
	return stmt.QueryRowContext(ctx, repo).Scan(&repo.ID, &repo.CreatedAt, &repo.UpdatedAt)
}

// GetRepositoryByFullName retrieves a repository by its full name.
func (s *postgresStore) GetRepositoryByFullName(ctx context.Context, fullName string) (*Repository, error) {
	query := `
SELECT id, full_name, clone_path, qdrant_collection_name, embedder_model_name, last_indexed_sha, created_at, updated_at 
FROM repositories 
WHERE full_name = $1`
	var repo Repository
	err := s.db.GetContext(ctx, &repo, query, fullName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get repository by full name %s: %w", fullName, err)
	}
	return &repo, nil
}

// UpdateRepository updates an existing repository record in the database.
func (s *postgresStore) UpdateRepository(ctx context.Context, repo *Repository) error {
	repo.UpdatedAt = time.Now()
	query := `
		UPDATE repositories 
		SET 
			clone_path = :clone_path, 
			qdrant_collection_name = :qdrant_collection_name, 
			last_indexed_sha = :last_indexed_sha, 
			updated_at = :updated_at 
		WHERE id = :id`

	_, err := s.db.NamedExecContext(ctx, query, repo)
	if err != nil {
		return fmt.Errorf("failed to update repository %q: %w", repo.FullName, err)
	}
	return nil
}

// GetAllReviewsForPR retrieves all reviews for a specific pull request from the database.
func (s *postgresStore) GetAllReviewsForPR(ctx context.Context, repoFullName string, prNumber int) ([]*core.Review, error) {
	query := `
		SELECT id, repo_full_name, pr_number, head_sha, review_content, created_at 
		FROM reviews 
		WHERE repo_full_name = $1 AND pr_number = $2 
		ORDER BY created_at ASC`

	var reviews []*core.Review
	err := s.db.SelectContext(ctx, &reviews, query, repoFullName, prNumber)
	if err != nil {
		slog.ErrorContext(ctx, "failed to retrieve all reviews for PR",
			"repo_full_name", repoFullName,
			"pr_number", prNumber,
			"error", err,
		)
		return nil, fmt.Errorf("failed to retrieve all reviews for %q PR %d: %w", repoFullName, prNumber, err)
	}

	return reviews, nil
}

// GetAllRepositories retrieves all non-deleted repositories from the database.
func (s *postgresStore) GetAllRepositories(ctx context.Context) ([]*Repository, error) {
	query := `
		SELECT id, full_name, clone_path, qdrant_collection_name, embedder_model_name, last_indexed_sha, created_at, updated_at
		FROM repositories
		ORDER BY full_name ASC`

	var repos []*Repository
	err := s.db.SelectContext(ctx, &repos, query)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to retrieve all repositories: %w", err)
	}
	return repos, nil
}

// GetRepositoryByClonePath retrieves a repository by its local clone path.
func (s *postgresStore) GetRepositoryByClonePath(ctx context.Context, clonePath string) (*Repository, error) {
	query := `
		SELECT id, full_name, clone_path, qdrant_collection_name, last_indexed_sha, embedder_model_name, created_at, updated_at
		FROM repositories
		WHERE clone_path = $1`

	var repo Repository
	err := s.db.GetContext(ctx, &repo, query, clonePath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get repository by clone path %s: %w", clonePath, err)
	}
	return &repo, nil
}
