package core

import "time"

// Review represents a single code review stored in the database.
type Review struct {
	ID            int64     `db:"id"`
	RepoFullName  string    `db:"repo_full_name"`
	PRNumber      int       `db:"pr_number"`
	HeadSHA       string    `db:"head_sha"`
	ReviewContent string    `db:"review_content"`
	CreatedAt     time.Time `db:"created_at"`
}

// ReReviewData is a type-safe struct for rendering re-review prompts.
// ReReviewData is a type-safe struct for rendering re-review prompts.
type ReReviewData struct {
	Language         string
	OriginalReview   string
	NewDiff          string
	UserInstructions string
	Context          string
}
