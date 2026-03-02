package tools

import "time"

// ReviewTracker tracks code review results for PR enforcement.
// This interface is implemented by the MCP Server to enforce review approval
// before allowing PR creation.
type ReviewTracker interface {
	// RecordReview stores the review result for enforcement.
	RecordReview(verdict, diffHash string)

	// GetLastReview returns the last review result.
	// Returns empty strings if no review has been recorded.
	GetLastReview() (verdict string, timestamp time.Time, diffHash string)

	// CheckApproval verifies if there's a recent approved review.
	// Returns error if no review found, review is not approved, or review is stale.
	CheckApproval(diffHash string) error
}
