package globalmcp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultWorkspaceTTL = 2 * time.Hour
	cleanupInterval     = 15 * time.Minute
	tokenLength         = 32
)

type WorkspaceInfo struct {
	Token       string
	MCPEndpoint string
	Repository  string
	SessionID   string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ProjectRoot string
	Metadata    WorkspaceMeta
}

type WorkspaceMeta struct {
	PRNumber    int
	IssueNumber int
	Branch      string
}

type WorkspaceRegistry struct {
	mu          sync.RWMutex
	workspaces  map[string]*WorkspaceInfo // indexed by token
	bySessionID map[string]string         // sessionID -> token mapping
	logger      *slog.Logger
	stopChan    chan struct{}
	stopped     bool
}

func NewWorkspaceRegistry(logger *slog.Logger) *WorkspaceRegistry {
	reg := &WorkspaceRegistry{
		workspaces:  make(map[string]*WorkspaceInfo),
		bySessionID: make(map[string]string),
		logger:      logger,
		stopChan:    make(chan struct{}),
	}

	go reg.cleanupLoop()

	return reg
}

func (r *WorkspaceRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopped {
		return
	}

	r.stopped = true
	close(r.stopChan)
	r.logger.Info("workspace registry closed")
}

func (r *WorkspaceRegistry) RegisterWorkspace(mcpEndpoint, repository, sessionID, projectRoot string, metadata WorkspaceMeta) (string, error) {
	token, err := r.generateToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	now := time.Now()
	info := &WorkspaceInfo{
		Token:       token,
		MCPEndpoint: mcpEndpoint,
		Repository:  repository,
		SessionID:   sessionID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(defaultWorkspaceTTL),
		ProjectRoot: projectRoot,
		Metadata:    metadata,
	}

	r.mu.Lock()
	r.workspaces[token] = info
	r.bySessionID[sessionID] = token
	r.mu.Unlock()

	r.logger.Info("workspace registered",
		"token", token[:8],
		"repository", repository,
		"session_id", sessionID,
		"expires", info.ExpiresAt.Format(time.RFC3339))

	return token, nil
}

func (r *WorkspaceRegistry) UnregisterWorkspace(token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.workspaces[token]
	if !exists {
		r.logger.Warn("attempted to unregister non-existent workspace", "token", token[:8])
		return fmt.Errorf("workspace not found: %s", token[:8])
	}

	delete(r.workspaces, token)
	delete(r.bySessionID, info.SessionID)
	r.logger.Info("workspace unregistered",
		"token", token[:8],
		"repository", info.Repository,
		"session_id", info.SessionID)

	return nil
}

func (r *WorkspaceRegistry) UnregisterWorkspaceBySessionID(sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	token, exists := r.bySessionID[sessionID]
	if !exists {
		r.logger.Warn("attempted to unregister non-existent workspace by session_id", "session_id", sessionID)
		return fmt.Errorf("workspace not found for session: %s", sessionID)
	}

	info, exists := r.workspaces[token]
	if !exists {
		delete(r.bySessionID, sessionID)
		r.logger.Warn("workspace token exists but workspace info missing", "session_id", sessionID, "token", token[:8])
		return fmt.Errorf("workspace info not found: %s", token[:8])
	}

	delete(r.workspaces, token)
	delete(r.bySessionID, sessionID)
	r.logger.Info("workspace unregistered by session_id",
		"token", token[:8],
		"repository", info.Repository,
		"session_id", sessionID)

	return nil
}

func (r *WorkspaceRegistry) GetWorkspace(token string) (*WorkspaceInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.workspaces[token]
	if !exists {
		return nil, fmt.Errorf("workspace not found: %s", token[:8])
	}

	if time.Now().After(info.ExpiresAt) {
		return nil, fmt.Errorf("workspace expired: %s", token[:8])
	}

	return info, nil
}

func (r *WorkspaceRegistry) ListWorkspaces() []*WorkspaceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	workspaces := make([]*WorkspaceInfo, 0, len(r.workspaces))
	for _, info := range r.workspaces {
		workspaces = append(workspaces, info)
	}
	return workspaces
}

func (r *WorkspaceRegistry) CleanupExpired() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cleaned := 0

	for token, info := range r.workspaces {
		if now.After(info.ExpiresAt) {
			delete(r.workspaces, token)
			delete(r.bySessionID, info.SessionID)
			cleaned++
			r.logger.Info("workspace expired and cleaned up",
				"token", token[:8],
				"repository", info.Repository,
				"session_id", info.SessionID)
		}
	}

	if cleaned > 0 {
		r.logger.Info("workspace cleanup completed", "removed", cleaned)
	}

	return cleaned
}

func (r *WorkspaceRegistry) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.CleanupExpired()
		case <-r.stopChan:
			r.logger.Info("workspace cleanup loop stopped")
			return
		}
	}
}

func (r *WorkspaceRegistry) generateToken() (string, error) {
	bytes := make([]byte, tokenLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
