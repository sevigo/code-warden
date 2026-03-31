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
	// ErrDuplicateReview is returned when attempting to save a review that already exists
	// for the same repository, PR number, and head SHA.
	ErrDuplicateReview = errors.New("review already exists for this PR/SHA")
)

// Repository represents a stored Git repository.
type Repository struct {
	ID                   int64        `json:"id" db:"id"`
	RepoID               int64        `json:"repo_id" db:"repo_id"`
	InstallationID       int64        `json:"installation_id" db:"installation_id"`
	FullName             string       `json:"full_name" db:"full_name"`
	ClonePath            string       `json:"clone_path" db:"clone_path"`
	QdrantCollectionName string       `json:"qdrant_collection_name" db:"qdrant_collection_name"`
	LastIndexedSHA       string       `json:"last_indexed_sha" db:"last_indexed_sha"`
	LastReviewDate       time.Time    `json:"last_review_date" db:"last_review_date"`
	GeneratedContext     string       `json:"generated_context" db:"generated_context"`
	ContextUpdatedAt     sql.NullTime `json:"context_updated_at" db:"context_updated_at"`
	CreatedAt            time.Time    `json:"created_at" db:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at" db:"updated_at"`
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

// JobRun represents a single job execution record.
type JobRun struct {
	ID           int64      `db:"id"`
	Type         string     `db:"type"`
	RepoFullName string     `db:"repo_full_name"`
	PRNumber     int        `db:"pr_number"`
	Status       string     `db:"status"`
	TriggeredBy  string     `db:"triggered_by"`
	TriggeredAt  time.Time  `db:"triggered_at"`
	CompletedAt  *time.Time `db:"completed_at"`
	DurationMs   *int64     `db:"duration_ms"`
}

// ReviewStats holds aggregate counts for the global stats endpoint.
type ReviewStats struct {
	TotalReviews    int
	ReviewsThisWeek int
}

// Store defines the interface for all database operations.
//
//go:generate mockgen -destination=../../mocks/mock_store.go -package=mocks github.com/sevigo/code-warden/internal/storage Store
type Store interface {
	SaveReview(ctx context.Context, review *core.Review) error
	GetLatestReviewForPR(ctx context.Context, repoFullName string, prNumber int) (*core.Review, error)
	GetAllReviewsForPR(ctx context.Context, repoFullName string, prNumber int) ([]*core.Review, error)
	GetReviewsForRepo(ctx context.Context, repoFullName string) ([]*core.Review, error)
	GetReviewStats(ctx context.Context) (*ReviewStats, error)
	CreateRepository(ctx context.Context, repo *Repository) error
	GetRepositoryByFullName(ctx context.Context, fullName string) (*Repository, error)
	GetRepositoryByClonePath(ctx context.Context, clonePath string) (*Repository, error)
	GetRepositoryByID(ctx context.Context, id int64) (*Repository, error)
	UpdateRepository(ctx context.Context, repo *Repository) error

	GetAllRepositories(ctx context.Context) ([]*Repository, error)

	// File tracking
	GetFilesForRepo(ctx context.Context, repoID int64) (map[string]FileRecord, error)
	UpsertFiles(ctx context.Context, repoID int64, files []FileRecord) error
	DeleteFiles(ctx context.Context, repoID int64, paths []string) error

	// Scan State
	GetScanState(ctx context.Context, repoID int64) (*ScanState, error)
	UpsertScanState(ctx context.Context, state *ScanState) error

	// Job runs
	InsertJobRun(ctx context.Context, job *JobRun) (int64, error)
	UpdateJobRun(ctx context.Context, id int64, status string, completedAt time.Time, durationMs int64) error
	ListJobRuns(ctx context.Context, limit, offset int) ([]*JobRun, error)
}

type postgresStore struct {
	db *sqlx.DB
}

// NewStore creates a new Store
func NewStore(db *sqlx.DB) Store {
	return &postgresStore{db: db}
}

// SaveReview inserts a new review record into the database.
// Returns ErrDuplicateReview if a review already exists for the same repo/PR/SHA combination.
func (s *postgresStore) SaveReview(ctx context.Context, review *core.Review) error {
	query := `
		INSERT INTO reviews (repo_full_name, pr_number, head_sha, review_content)
		VALUES ($1, $2, $3, $4)`
	_, err := s.db.ExecContext(ctx, query, review.RepoFullName, review.PRNumber, review.HeadSHA, review.ReviewContent)
	if err != nil {
		// Check for PostgreSQL unique constraint violation (error code 23505)
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return ErrDuplicateReview
		}
		return err
	}
	return nil
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
		INSERT INTO repositories (full_name, clone_path, qdrant_collection_name, last_indexed_sha, generated_context, context_updated_at, installation_id) 
		VALUES (:full_name, :clone_path, :qdrant_collection_name, :last_indexed_sha, :generated_context, :context_updated_at, :installation_id) 
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
SELECT id, full_name, clone_path, qdrant_collection_name, last_indexed_sha, generated_context, context_updated_at, created_at, updated_at, installation_id 
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
	query := `
		UPDATE repositories 
		SET 
			clone_path = :clone_path, 
			qdrant_collection_name = :qdrant_collection_name, 
			last_indexed_sha = :last_indexed_sha,
			generated_context = :generated_context,
			context_updated_at = :context_updated_at,
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
		SELECT id, full_name, clone_path, qdrant_collection_name, last_indexed_sha, generated_context, context_updated_at, created_at, updated_at, installation_id
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
		SELECT id, full_name, clone_path, qdrant_collection_name, last_indexed_sha, generated_context, context_updated_at, created_at, updated_at, installation_id
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

// GetRepositoryByID retrieves a repository by its primary key ID.
func (s *postgresStore) GetRepositoryByID(ctx context.Context, id int64) (*Repository, error) {
	query := `
		SELECT id, full_name, clone_path, qdrant_collection_name, last_indexed_sha, generated_context, context_updated_at, created_at, updated_at, installation_id
		FROM repositories
		WHERE id = $1`

	var repo Repository
	err := s.db.GetContext(ctx, &repo, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get repository by id %d: %w", id, err)
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
	defer rows.Close()

	files := make(map[string]FileRecord)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var record FileRecord
		if err := rows.StructScan(&record); err != nil {
			return nil, fmt.Errorf("failed to scan file record: %w", err)
		}
		files[record.FilePath] = record
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed iterating file records for repo %d: %w", repoID, err)
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

// GetReviewsForRepo retrieves all reviews for a repository ordered by most recent first.
func (s *postgresStore) GetReviewsForRepo(ctx context.Context, repoFullName string) ([]*core.Review, error) {
	query := `
		SELECT id, repo_full_name, pr_number, head_sha, review_content, created_at
		FROM reviews
		WHERE repo_full_name = $1
		ORDER BY created_at DESC`

	var reviews []*core.Review
	err := s.db.SelectContext(ctx, &reviews, query, repoFullName)
	if err != nil {
		return nil, fmt.Errorf("failed to get reviews for repo %q: %w", repoFullName, err)
	}
	return reviews, nil
}

// GetReviewStats returns aggregate review counts for the global stats endpoint.
func (s *postgresStore) GetReviewStats(ctx context.Context) (*ReviewStats, error) {
	query := `
		SELECT
			COUNT(*) AS total_reviews,
			COUNT(*) FILTER (WHERE created_at >= NOW() - INTERVAL '7 days') AS reviews_this_week
		FROM reviews`

	var stats ReviewStats
	row := s.db.QueryRowContext(ctx, query)
	if err := row.Scan(&stats.TotalReviews, &stats.ReviewsThisWeek); err != nil {
		return nil, fmt.Errorf("failed to get review stats: %w", err)
	}
	return &stats, nil
}

// InsertJobRun inserts a new job run record and returns its ID.
func (s *postgresStore) InsertJobRun(ctx context.Context, job *JobRun) (int64, error) {
	query := `
		INSERT INTO job_runs (type, repo_full_name, pr_number, status, triggered_by, triggered_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`

	var id int64
	err := s.db.QueryRowContext(ctx, query,
		job.Type, job.RepoFullName, job.PRNumber, job.Status, job.TriggeredBy, job.TriggeredAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to insert job run: %w", err)
	}
	return id, nil
}

// UpdateJobRun updates the status, completion time, and duration of a job run.
func (s *postgresStore) UpdateJobRun(ctx context.Context, id int64, status string, completedAt time.Time, durationMs int64) error {
	query := `
		UPDATE job_runs
		SET status = $1, completed_at = $2, duration_ms = $3
		WHERE id = $4`

	_, err := s.db.ExecContext(ctx, query, status, completedAt, durationMs, id)
	if err != nil {
		return fmt.Errorf("failed to update job run %d: %w", id, err)
	}
	return nil
}

// ListJobRuns retrieves job runs ordered by most recent first.
func (s *postgresStore) ListJobRuns(ctx context.Context, limit, offset int) ([]*JobRun, error) {
	query := `
		SELECT id, type, repo_full_name, pr_number, status, triggered_by, triggered_at, completed_at, duration_ms
		FROM job_runs
		ORDER BY triggered_at DESC
		LIMIT $1 OFFSET $2`

	var jobs []*JobRun
	err := s.db.SelectContext(ctx, &jobs, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list job runs: %w", err)
	}
	return jobs, nil
}
