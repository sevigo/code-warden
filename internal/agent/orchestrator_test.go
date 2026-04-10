package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSpawnAgent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	o := &Orchestrator{
		logger:   logger,
		sessions: make(map[string]*Session),
		config:   DefaultConfig(),
	}

	// Test disabled
	ctx := context.Background()
	issue := Issue{Number: 1, Title: "Test Issue"}
	_, err := o.SpawnAgent(ctx, issue)
	if err == nil || err.Error() != "agent functionality is disabled" {
		t.Errorf("SpawnAgent() expected error 'agent functionality is disabled', got %v", err)
	}

	// Test enabled and concurrency
	o.config.Enabled = true
	o.config.MaxConcurrentSessions = 1

	s1, err := o.SpawnAgent(ctx, issue)
	if err != nil {
		t.Fatalf("SpawnAgent() unexpected error: %v", err)
	}
	if s1.GetStatus() != StatusPending {
		t.Errorf("SpawnAgent() expected status %v, got %v", StatusPending, s1.GetStatus())
	}

	_, err = o.SpawnAgent(ctx, issue)
	if err == nil || !reflect.DeepEqual(err.Error(), "maximum concurrent sessions reached (1), please retry later") {
		t.Errorf("SpawnAgent() expected concurrency error, got %v", err)
	}
}

func TestGetSession(t *testing.T) {
	o := &Orchestrator{
		sessions: make(map[string]*Session),
	}
	session := &Session{ID: "test-id"}
	o.sessions["test-id"] = session

	s, ok := o.GetSession("test-id")
	if !ok || s.ID != "test-id" {
		t.Errorf("GetSession() failed to retrieve session")
	}

	_, ok = o.GetSession("non-existent")
	if ok {
		t.Errorf("GetSession() retrieved non-existent session")
	}
}

func TestCancelSession(t *testing.T) {
	o := &Orchestrator{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: make(map[string]*Session),
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{
		ID:     "test-id",
		cancel: cancel,
		status: StatusRunning,
	}
	o.sessions["test-id"] = session

	err := o.CancelSession("test-id")
	if err != nil {
		t.Fatalf("CancelSession() error: %v", err)
	}

	if session.GetStatus() != StatusCancelled {
		t.Errorf("CancelSession() expected status %v, got %v", StatusCancelled, session.GetStatus())
	}
	if ctx.Err() == nil {
		t.Errorf("CancelSession() failed to cancel context")
	}
}

func TestCleanupOldSessions(t *testing.T) {
	o := &Orchestrator{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: make(map[string]*Session),
		config:   DefaultConfig(),
	}
	o.config.WorkingDir = t.TempDir()

	// 1. Session to be cleaned up
	oldID := "old-session"
	oldWorkspace := filepath.Join(o.config.WorkingDir, oldID)
	_ = os.MkdirAll(oldWorkspace, 0750)
	o.sessions[oldID] = &Session{
		ID:          oldID,
		status:      StatusCompleted,
		CompletedAt: time.Now().Add(-2 * time.Hour),
	}

	// 2. Recent session (should stay)
	recentID := "recent-session"
	o.sessions[recentID] = &Session{
		ID:          recentID,
		status:      StatusCompleted,
		CompletedAt: time.Now().Add(-10 * time.Minute),
	}

	// 3. Running session (should stay)
	runningID := "running-session"
	o.sessions[runningID] = &Session{
		ID:     runningID,
		status: StatusRunning,
	}

	o.cleanupOldSessions()

	if _, ok := o.sessions[oldID]; ok {
		t.Errorf("cleanupOldSessions() failed to remove old session")
	}
	if _, ok := o.sessions[recentID]; !ok {
		t.Errorf("cleanupOldSessions() removed recent session")
	}
	if _, ok := o.sessions[runningID]; !ok {
		t.Errorf("cleanupOldSessions() removed running session")
	}

	// Verify workspace cleanup
	if _, err := os.Stat(oldWorkspace); !os.IsNotExist(err) {
		t.Errorf("cleanupOldSessions() failed to remove old workspace directory")
	}
}

func TestReadLogFile_Capping(t *testing.T) {
	o := &Orchestrator{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "large.log")

	// Create a log file larger than the cap
	capSize := int64(50)
	sentinel := "AGENT_RESULT: {\"pr_number\": 123}"
	// Total size will be 100 + len(sentinel)
	content := strings.Repeat("A", 100) + sentinel
	err := os.WriteFile(logPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to create mock log file: %v", err)
	}

	got, err := o.readLogFile(logPath, capSize)
	if err != nil {
		t.Fatalf("readLogFile() error: %v", err)
	}

	if int64(len(got)) != capSize {
		t.Errorf("readLogFile() expected length %d, got %d", capSize, len(got))
	}

	// Verify it contains the end of the sentinel which was at the end of the file
	if !strings.Contains(string(got), "AGENT_RESULT:") {
		t.Errorf("readLogFile() failed to capture trailing sentinel. Content: %s", string(got))
	}
}
