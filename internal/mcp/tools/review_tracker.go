package tools

import (
	"context"
	"time"
)

// ReviewTracker tracks code review results for PR enforcement.
// This interface is implemented by the MCP Server to enforce review approval
// before allowing PR creation.
//
// Session-scoped methods (RecordReviewBySession, CheckApprovalBySession) use the
// session ID from context to scope results per agent session, preventing race
// conditions when multiple sessions run concurrently. The session ID is injected
// into the tool context by the MCP server's handleMessage before tool execution.
//
// The legacy non-session methods (RecordReview, CheckApproval, GetLastReview)
// are kept for backward compatibility and fall back to global state when no
// session ID is present in the context.
type ReviewTracker interface {
	// RecordReviewBySession stores the review result scoped to the session in ctx.
	// Falls back to global state when no session ID is present.
	RecordReviewBySession(ctx context.Context, verdict, diffHash string)

	// CheckApprovalBySession verifies there is a recent approved review for the
	// session in ctx. Falls back to global state when no session ID is present.
	CheckApprovalBySession(ctx context.Context, diffHash string) error

	// GetLastReview returns the last review result (global, for diagnostics).
	GetLastReview() (verdict string, timestamp time.Time, diffHash string)

	// RecordReviewFiles stores the list of files reviewed in the current session.
	// The session ID is read from ctx to scope the files per session, preventing
	// concurrent sessions from overwriting each other's file lists.
	RecordReviewFiles(ctx context.Context, files []string)

	// GetLastReviewFiles returns the files from the last review (global fallback).
	GetLastReviewFiles() []string
}
