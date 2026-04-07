package mcp

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"

	"github.com/sevigo/code-warden/internal/mcp/tools"
)

func newTestServer() *Server {
	return &Server{
		registry:         goframeagent.NewRegistry(),
		logger:           slog.New(slog.NewTextHandler(os.Stderr, nil)),
		sessions:         make(map[string]*sseSession),
		workspaces:       make(map[string]string),
		reviewsBySession: make(map[string]*reviewResult),
	}
}

func TestRecordReviewBySession_ScopedToSession(t *testing.T) {
	s := newTestServer()

	ctxA := tools.WithSessionID(context.Background(), "session-A")
	ctxB := tools.WithSessionID(context.Background(), "session-B")

	s.RecordReviewBySession(ctxA, "APPROVE", "hash-a")
	s.RecordReviewBySession(ctxB, "REQUEST_CHANGES", "hash-b")

	// Session A should see APPROVE
	if err := s.CheckApprovalBySession(ctxA, "hash-a"); err != nil {
		t.Errorf("session A should be approved, got: %v", err)
	}

	// Session B should see REQUEST_CHANGES (not approved)
	if err := s.CheckApprovalBySession(ctxB, "hash-b"); err == nil {
		t.Error("session B should not be approved")
	}
}

func TestRecordReviewBySession_EmptySessionIDFallsBackToGlobal(t *testing.T) {
	s := newTestServer()

	// Record with no session ID → stored globally only
	s.RecordReviewBySession(context.Background(), "APPROVE", "global-hash")

	// Check with no session ID → should fall back to global state
	if err := s.CheckApprovalBySession(context.Background(), "global-hash"); err != nil {
		t.Errorf("global fallback check failed: %v", err)
	}

	// Check with an unknown session ID → should fall back to global state
	ctxUnknown := tools.WithSessionID(context.Background(), "unknown-session")
	if err := s.CheckApprovalBySession(ctxUnknown, "global-hash"); err != nil {
		t.Errorf("unknown session should fall back to global: %v", err)
	}
}

func TestClearReviewBySession(t *testing.T) {
	s := newTestServer()

	ctx := tools.WithSessionID(context.Background(), "sess-1")
	s.RecordReviewBySession(ctx, "APPROVE", "h1")

	if _, ok := s.reviewsBySession["sess-1"]; !ok {
		t.Fatal("session review should exist after recording")
	}

	s.ClearReviewBySession("sess-1")

	if _, ok := s.reviewsBySession["sess-1"]; ok {
		t.Error("session review should be removed after clear")
	}
}

func TestClearReviewBySession_EmptyID(_ *testing.T) {
	s := newTestServer()
	// Should not panic on empty session ID
	s.ClearReviewBySession("")
}

func TestCleanupStaleSessionReviews(t *testing.T) {
	s := newTestServer()

	// Insert a stale entry (older than maxReviewAge)
	s.reviewsBySession["old-session"] = &reviewResult{
		Verdict:   "APPROVE",
		Timestamp: time.Now().Add(-2 * maxReviewAge),
	}
	// Insert a fresh entry
	s.reviewsBySession["new-session"] = &reviewResult{
		Verdict:   "APPROVE",
		Timestamp: time.Now(),
	}

	s.cleanupStaleSessionReviews()

	if _, ok := s.reviewsBySession["old-session"]; ok {
		t.Error("stale session review should have been evicted")
	}
	if _, ok := s.reviewsBySession["new-session"]; !ok {
		t.Error("fresh session review should be retained")
	}
}

func TestUnregisterWorkspace_ClearsReview(t *testing.T) {
	s := newTestServer()
	s.workspaces["ws-token"] = "/tmp/test"

	// Record a review for this session token
	ctx := tools.WithSessionID(context.Background(), "ws-token")
	s.RecordReviewBySession(ctx, "APPROVE", "hash")

	if _, ok := s.reviewsBySession["ws-token"]; !ok {
		t.Fatal("review should exist before unregister")
	}

	s.UnregisterWorkspace("ws-token")

	if _, ok := s.reviewsBySession["ws-token"]; ok {
		t.Error("review should be cleared after workspace unregistration")
	}
}

func TestGetReviewFilesBySession_FallsBackToGlobal(t *testing.T) {
	s := newTestServer()

	// Record files globally (no session ID)
	s.RecordReviewFiles(context.Background(), []string{"a.go", "b.go"})

	// Unknown session should fall back to global files
	files := s.GetReviewFilesBySession("unknown")
	if len(files) != 2 {
		t.Errorf("expected 2 files from global fallback, got %d", len(files))
	}
}

func TestGetReviewFilesBySession_SessionScoped(t *testing.T) {
	s := newTestServer()

	ctxA := tools.WithSessionID(context.Background(), "A")
	ctxB := tools.WithSessionID(context.Background(), "B")

	s.RecordReviewFiles(ctxA, []string{"a.go"})
	s.RecordReviewFiles(ctxB, []string{"b.go", "c.go"})

	filesA := s.GetReviewFilesBySession("A")
	filesB := s.GetReviewFilesBySession("B")

	if len(filesA) != 1 || filesA[0] != "a.go" {
		t.Errorf("session A files wrong: %v", filesA)
	}
	if len(filesB) != 2 {
		t.Errorf("session B files wrong: %v", filesB)
	}
}
