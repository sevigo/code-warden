package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/sevigo/code-warden/internal/core"
)

var (
	// ErrNotFound is returned when a requested record is not found in the database.
	ErrNotFound = errors.New("record not found")
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

// FileRecord represents a tracked file in a repository.
type FileRecord struct {
	ID            int64     `db:"id"`
	RepositoryID  int64     `db:"repository_id"`
	FilePath      string    `db:"file_path"`
	FileHash      string    `db:"file_hash"`
	LastIndexedAt time.Time `db:"last_indexed_at"`
}

// ScanState represents the state of a scan process.
type ScanState struct {
	ID           int64            `db:"id"`
	RepositoryID int64            `db:"repository_id"`
	Status       string           `db:"status"`
	Progress     json.RawMessage  `db:"progress"`
	Artifacts    *json.RawMessage `db:"artifacts"`
	CreatedAt    time.Time        `db:"created_at"`
	UpdatedAt    time.Time        `db:"updated_at"`
}

// Store defines the interface for all database operations.
//
//go:generate mockgen -destination=../../mocks/mock_store.go -package=mocks github.com/sevigo/code-warden/internal/storage Store
type Store interface {
	SaveReview(ctx context.Context, review *core.Review) error
	GetLatestReviewForPR(ctx context.Context, repoFullName string, prNumber int) (*core.Review, error)
	GetAllReviewsForPR(ctx context.Context, repoFullName string, prNumber int) ([]*core.Review, error)
	CreateRepository(ctx context.Context, repo *Repository) error
	GetRepositoryByFullName(ctx context.Context, fullName string) (*Repository, error)
	GetRepositoryByClonePath(ctx context.Context, clonePath string) (*Repository, error)
	UpdateRepository(ctx context.Context, repo *Repository) error

	GetAllRepositories(ctx context.Context) ([]*Repository, error)

	// File tracking
	GetFilesForRepo(ctx context.Context, repoID int64) (map[string]FileRecord, error)
	UpsertFiles(ctx context.Context, repoID int64, files []FileRecord) error
	DeleteFiles(ctx context.Context, repoID int64, paths []string) error

	// Scan State
	GetScanState(ctx context.Context, repoID int64) (*ScanState, error)
	UpsertScanState(ctx context.Context, state *ScanState) error
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
			return nil, ErrNotFound
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
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get repository by full name %s: %w", fullName, err)
	}
	return &repo, nil
}

// UpdateRepository updates an existing repository record in the database.
func (s *postgresStore) UpdateRepository(ctx context.Context, repo *Repository) error {
	// repo.UpdatedAt is handled by DB NOW()
	query := `
		UPDATE repositories 
		SET 
			clone_path = :clone_path, 
			qdrant_collection_name = :qdrant_collection_name, 
			embedder_model_name = :embedder_model_name,
			last_indexed_sha = :last_indexed_sha, 
			updated_at = NOW() 
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
	if err != nil {
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
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get repository by clone path %s: %w", clonePath, err)
	}
	return &repo, nil
}

// GetFilesForRepo returns a map of file_path -> FileRecord for a repository.
func (s *postgresStore) GetFilesForRepo(ctx context.Context, repoID int64) (map[string]FileRecord, error) {
	query := `SELECT id, repository_id, file_path, file_hash, last_indexed_at FROM repository_files WHERE repository_id = $1`
	rows, err := s.db.QueryxContext(ctx, query, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list files for repo %d: %w", repoID, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			slog.ErrorContext(ctx, "failed to close rows in GetFilesForRepo", "error", err)
		}
	}()

	files := make(map[string]FileRecord)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var record FileRecord
		if err := rows.StructScan(&record); err != nil {
			return nil, err
		}
		files[record.FilePath] = record
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return files, nil
}

// UpsertFiles updates or inserts file tracking records in bulk.
func (s *postgresStore) UpsertFiles(ctx context.Context, repoID int64, files []FileRecord) error {
	if len(files) == 0 {
		return nil
	}

	// Use a transaction for bulk insert
	const batchSize = 1000
	for i := 0; i < len(files); i += batchSize {
		end := i + batchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[i:end]

		if err := s.upsertFilesBatch(ctx, repoID, batch); err != nil {
			return fmt.Errorf("failed to upsert batch %d-%d: %w", i, end, err)
		}
	}

	return nil
}

func (s *postgresStore) upsertFilesBatch(ctx context.Context, repoID int64, files []FileRecord) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			slog.ErrorContext(ctx, "transaction rollback failed in UpsertFiles", "error", err)
		}
	}()

	// Prepare statement for bulk upsert
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO repository_files (repository_id, file_path, file_hash, last_indexed_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (repository_id, file_path) 
		DO UPDATE SET file_hash = EXCLUDED.file_hash, last_indexed_at = NOW()
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare upsert stmt: %w", err)
	}
	defer stmt.Close()

	for _, f := range files {
		if _, err := stmt.ExecContext(ctx, repoID, f.FilePath, f.FileHash); err != nil {
			return fmt.Errorf("failed to upsert file %s: %w", f.FilePath, err)
		}
	}

	return tx.Commit()
}

// DeleteFiles removes file tracking records.
func (s *postgresStore) DeleteFiles(ctx context.Context, repoID int64, paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	const batchSize = 1000
	for i := 0; i < len(paths); i += batchSize {
		end := i + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		batch := paths[i:end]

		query, args, err := sqlx.In("DELETE FROM repository_files WHERE repository_id = ? AND file_path IN (?)", repoID, batch)
		if err != nil {
			return fmt.Errorf("failed to build delete query: %w", err)
		}
		query = s.db.Rebind(query)

		_, err = s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("failed to delete files batch for repo %d: %w", repoID, err)
		}
	}
	return nil
}

// GetScanState retrieves the scan state for a repository.
func (s *postgresStore) GetScanState(ctx context.Context, repoID int64) (*ScanState, error) {
	query := `SELECT id, repository_id, status, progress, artifacts, created_at, updated_at FROM scan_state WHERE repository_id = $1`
	var state ScanState
	err := s.db.GetContext(ctx, &state, query, repoID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get scan state for repo %d: %w", repoID, err)
	}
	return &state, nil
}

// UpsertScanState updates or inserts a scan state record.
func (s *postgresStore) UpsertScanState(ctx context.Context, state *ScanState) error {
	query := `
		INSERT INTO scan_state (repository_id, status, progress, artifacts, updated_at)
		VALUES (:repository_id, :status, :progress, :artifacts, NOW())
		ON CONFLICT (repository_id)
		DO UPDATE SET status = EXCLUDED.status, progress = EXCLUDED.progress, artifacts = EXCLUDED.artifacts, updated_at = NOW()
		RETURNING id, created_at, updated_at`

	rows, err := s.db.NamedQueryContext(ctx, query, state)
	if err != nil {
		// Checks for specific pq errors if needed, but for now generic error
		var pqErr *pq.Error
		if errors.As(err, &pqErr) {
			slog.Error("postgres error during upsert scan state", "code", pqErr.Code, "message", pqErr.Message)
		}
		return fmt.Errorf("failed to upsert scan state for repo %d: %w", state.RepositoryID, err)
	}
	defer rows.Close()

	if rows.Next() {
		if err := rows.Scan(&state.ID, &state.CreatedAt, &state.UpdatedAt); err != nil {
			return fmt.Errorf("failed to scan returned id/dates: %w", err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	return nil
}
