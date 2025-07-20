package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	// import db drivers
	_ "github.com/lib/pq"
	"github.com/sevigo/code-warden/internal/core"
)

// Store defines the interface for all database operations.
type Store interface {
	SaveReview(ctx context.Context, review *core.Review) error
	GetLatestReviewForPR(ctx context.Context, repoFullName string, prNumber int) (*core.Review, error)
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
	query := `INSERT INTO reviews (repo_full_name, pr_number, head_sha, review_content, created_at) VALUES ($1, $2, $3, $4, $5)`
	_, err := s.db.ExecContext(ctx, query, review.RepoFullName, review.PRNumber, review.HeadSHA, review.ReviewContent, time.Now())
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
