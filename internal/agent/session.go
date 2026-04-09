package agent

import (
	"context"
	"sync"
	"time"
)

// SessionStatus represents the status of an agent session.
type SessionStatus string

const (
	StatusPending   SessionStatus = "pending"
	StatusRunning   SessionStatus = "running"
	StatusCompleted SessionStatus = "completed"
	StatusFailed    SessionStatus = "failed"
	StatusCancelled SessionStatus = "cancelled"
	// StatusDraft indicates the agent made partial progress but could not reach
	// APPROVE. A draft PR was pushed to allow human review and continuation.
	StatusDraft SessionStatus = "draft"
)

// Session represents an active agent session.
type Session struct {
	mu          sync.Mutex
	ID          string
	Issue       Issue
	status      SessionStatus
	StartedAt   time.Time
	CompletedAt time.Time
	Result      *Result
	err         string
	cancel      context.CancelFunc
}

// SessionSnapshot is an immutable snapshot of session state.
type SessionSnapshot struct {
	ID          string
	Status      SessionStatus
	StartedAt   time.Time
	CompletedAt time.Time
	Error       string
	Result      *Result
}

// GetStatus returns the current session status (thread-safe).
func (s *Session) GetStatus() SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// SetStatus updates the session status (thread-safe).
func (s *Session) SetStatus(status SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// GetError returns the session error message (thread-safe).
func (s *Session) GetError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// SetError sets the session error message (thread-safe).
func (s *Session) SetError(err string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// GetResult returns the session result (thread-safe).
func (s *Session) GetResult() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Result
}

// SetResult sets the session result (thread-safe).
func (s *Session) SetResult(result *Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Result = result
}

// Snapshot returns a thread-safe copy of session state for external reading.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionSnapshot{
		ID:          s.ID,
		Status:      s.status,
		StartedAt:   s.StartedAt,
		CompletedAt: s.CompletedAt,
		Error:       s.err,
		Result:      s.Result,
	}
}
