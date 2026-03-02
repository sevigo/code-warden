package tools

import "time"

// ReviewTracker tracks code review results for PR enforcement.
// This interface is implemented by the MCP Server to enforce review approval
// before allowing PR creation.
//
// Security Model:
// The review workflow is designed for a trusted agent context where the agent
// follows the prescribed workflow (review_code -> APPROVE -> create_pull_request).
// There is a theoretical race condition between CheckApproval() returning and
// the actual PR creation - a determined actor could potentially modify code
// between these steps. The diff_hash parameter helps detect if code changed
// after review, but this is not a complete security boundary.
//
// Hash Usage:
// The diffHash is a SHA-256 hash (64 hex characters) stored in full but logged
// with only the first 8 characters for readability. The full hash is used for
// comparison to detect any changes to the reviewed code. Note that hash-based
// detection is whitespace-sensitive - even minor formatting changes will
// invalidate the hash.
type ReviewTracker interface {
	// RecordReview stores the review result for enforcement.
	// The diffHash is the SHA-256 hash of the reviewed diff.
	RecordReview(verdict, diffHash string)

	// GetLastReview returns the last review result.
	// Returns empty strings if no review has been recorded.
	// This method is useful for diagnostics and debugging.
	GetLastReview() (verdict string, timestamp time.Time, diffHash string)

	// CheckApproval verifies if there's a recent approved review.
	// Returns error if no review found, review is not approved, or review is stale.
	// The diffHash parameter should match the hash of the current diff to detect changes.
	CheckApproval(diffHash string) error
}

// Note: The Server in internal/mcp package implements this interface.
// We cannot use the standard compile-time check pattern here due to
// the circular import between mcp and tools packages.