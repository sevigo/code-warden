// Package agent provides orchestration for AI coding agents.
// It manages agent sessions, spawns OpenCode processes, and handles the
// communication between code-warden and the agent.
package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
)

// Orchestrator manages agent sessions and their lifecycle.
type Orchestrator struct {
	store       storage.Store
	vectorStore storage.ScopedVectorStore
	ragService  rag.Service
	ghClient    github.Client
	mcpServer   *mcp.Server
	httpServer  *http.Server
	logger      *slog.Logger
	config      Config
	projectRoot string // Path to the repository being worked on

	sessions   map[string]*Session
	sessionsMu sync.RWMutex
}

// Config holds configuration for the agent orchestrator.
type Config struct {
	// Enabled determines if agent functionality is active.
	Enabled bool `yaml:"enabled"`

	// Provider is the agent provider (currently only "opencode").
	Provider string `yaml:"provider"`

	// Model is the Ollama model to use.
	Model string `yaml:"model"`

	// Timeout is the maximum time for an agent session.
	Timeout time.Duration `yaml:"timeout"`

	// MaxIterations is the maximum review iterations before escalation.
	MaxIterations int `yaml:"max_iterations"`

	// MCPAddr is the address for the MCP server.
	MCPAddr string `yaml:"mcp_addr"`

	// OpencodeAddr is the address for the OpenCode HTTP API.
	OpencodeAddr string `yaml:"opencode_addr"`

	// WorkingDir is the directory for agent workspaces.
	WorkingDir string `yaml:"working_dir"`
}

// DefaultConfig returns default configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:       false,
		Provider:      "opencode",
		Model:         "llama3.1:70b",
		Timeout:       30 * time.Minute,
		MaxIterations: 3,
		MCPAddr:       "127.0.0.1:8081", // Bind to localhost only for security
		OpencodeAddr:  "http://127.0.0.1:8000",
		WorkingDir:    "/tmp/code-warden-agents",
	}
}

// NewOrchestrator creates a new agent orchestrator.
func NewOrchestrator(
	store storage.Store,
	vectorStore storage.ScopedVectorStore,
	ragService rag.Service,
	ghClient github.Client,
	repo *storage.Repository,
	repoConfig *core.RepoConfig,
	projectRoot string,
	config Config,
	logger *slog.Logger,
) *Orchestrator {
	// Create MCP server
	mcpServer := mcp.NewServer(
		store,
		vectorStore,
		ragService,
		ghClient,
		repo,
		repoConfig,
		projectRoot,
		logger,
	)

	return &Orchestrator{
		store:       store,
		vectorStore: vectorStore,
		ragService:  ragService,
		ghClient:    ghClient,
		mcpServer:   mcpServer,
		logger:      logger,
		config:      config,
		projectRoot: projectRoot,
		sessions:    make(map[string]*Session),
	}
}

// Start begins the MCP HTTP server. Must be called before agents can use tools.
func (o *Orchestrator) Start() error {
	if !o.config.Enabled {
		o.logger.Info("agent orchestrator is disabled, not starting MCP server")
		return nil
	}

	o.httpServer = &http.Server{
		Addr:              o.config.MCPAddr,
		Handler:           o.mcpServer,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0, // Disable for long-lived SSE connections
		IdleTimeout:       120 * time.Second,
	}

	// Use a channel to signal when the server is ready
	ready := make(chan struct{})

	go func() {
		// Create a listener to know when the server is actually bound
		listenConfig := net.ListenConfig{}
		ln, err := listenConfig.Listen(context.Background(), "tcp", o.config.MCPAddr)
		if err != nil {
			o.logger.Error("failed to create MCP server listener", "error", err, "addr", o.config.MCPAddr)
			close(ready)
			return
		}
		close(ready)

		o.logger.Info("starting MCP HTTP server",
			"addr", o.config.MCPAddr,
			"provider", o.config.Provider,
			"model", o.config.Model)

		if err := o.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			o.logger.Error("MCP HTTP server failed", "error", err, "addr", o.config.MCPAddr)
		}
	}()

	// Wait for the server to be ready (with timeout)
	select {
	case <-ready:
		// Small delay to ensure the server is fully ready
		time.Sleep(100 * time.Millisecond)
		o.logger.Info("MCP HTTP server is ready", "addr", o.config.MCPAddr)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout waiting for MCP server to start")
	}

	// Start session cleanup goroutine
	go o.cleanupLoop()

	return nil
}

// Shutdown gracefully stops the MCP server and cleans up resources.
func (o *Orchestrator) Shutdown(ctx context.Context) error {
	o.logger.Info("shutting down agent orchestrator")

	// Cancel all running sessions
	o.sessionsMu.Lock()
	for _, session := range o.sessions {
		if session.cancel != nil {
			session.cancel()
		}
	}
	o.sessionsMu.Unlock()

	// Shutdown HTTP server
	if o.httpServer != nil {
		if err := o.httpServer.Shutdown(ctx); err != nil {
			o.logger.Error("MCP server shutdown error", "error", err)
			return err
		}
	}

	o.logger.Info("agent orchestrator shutdown complete")
	return nil
}

// cleanupLoop periodically removes old completed/failed sessions.
func (o *Orchestrator) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		o.cleanupOldSessions()
	}
}

// cleanupOldSessions removes sessions that have been completed for more than 1 hour.
func (o *Orchestrator) cleanupOldSessions() {
	o.sessionsMu.Lock()
	defer o.sessionsMu.Unlock()

	cutoff := time.Now().Add(-1 * time.Hour)
	removed := 0

	for id, session := range o.sessions {
		status := session.GetStatus()
		if status == StatusCompleted || status == StatusFailed || status == StatusCancelled {
			snapshot := session.Snapshot()
			if snapshot.CompletedAt.Before(cutoff) {
				delete(o.sessions, id)
				removed++
			}
		}
	}

	if removed > 0 {
		o.logger.Info("cleaned up old sessions", "count", removed, "remaining", len(o.sessions))
	}
}

// DeleteSession removes a session from the map.
func (o *Orchestrator) DeleteSession(id string) {
	o.sessionsMu.Lock()
	defer o.sessionsMu.Unlock()
	delete(o.sessions, id)
}

// MCPServer returns the MCP server instance for external routing if needed.
func (o *Orchestrator) MCPServer() *mcp.Server {
	return o.mcpServer
}

// Issue represents a GitHub issue to be implemented.
type Issue struct {
	Number       int
	Title        string
	Body         string
	Instructions string // Additional instructions from /implement comment
	RepoOwner    string
	RepoName     string
}

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

// SessionSnapshot is an immutable snapshot of session state.
type SessionSnapshot struct {
	ID          string
	Status      SessionStatus
	StartedAt   time.Time
	CompletedAt time.Time
	Error       string
	Result      *Result
}

// SessionStatus represents the status of an agent session.
type SessionStatus string

const (
	StatusPending   SessionStatus = "pending"
	StatusRunning   SessionStatus = "running"
	StatusReviewing SessionStatus = "reviewing"
	StatusCompleted SessionStatus = "completed"
	StatusFailed    SessionStatus = "failed"
	StatusCancelled SessionStatus = "cancelled"
)

// Result represents the result of an agent session.
type Result struct {
	PRNumber      int      `json:"pr_number"`
	PRURL         string   `json:"pr_url"`
	Branch        string   `json:"branch"`
	FilesChanged  []string `json:"files_changed"`
	ReviewSummary string   `json:"review_summary"`
	Verdict       string   `json:"verdict"`
	Iterations    int      `json:"iterations"`
}

// SpawnAgent creates a new agent session to implement an issue.
func (o *Orchestrator) SpawnAgent(ctx context.Context, issue Issue) (*Session, error) {
	if !o.config.Enabled {
		return nil, fmt.Errorf("agent functionality is disabled")
	}

	sessionID := generateSessionID()
	session := &Session{
		ID:        sessionID,
		Issue:     issue,
		status:    StatusPending,
		StartedAt: time.Now(),
	}

	o.sessionsMu.Lock()
	o.sessions[sessionID] = session
	o.sessionsMu.Unlock()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	session.cancel = cancel

	// Start agent in background
	go o.runAgent(ctx, session)

	o.logger.Info("agent session started",
		"session_id", sessionID,
		"issue", issue.Number,
		"repo", issue.RepoOwner+"/"+issue.RepoName)

	return session, nil
}

// GetSession retrieves a session by ID.
func (o *Orchestrator) GetSession(id string) (*Session, bool) {
	o.sessionsMu.RLock()
	defer o.sessionsMu.RUnlock()
	session, ok := o.sessions[id]
	return session, ok
}

// CancelSession cancels a running session.
func (o *Orchestrator) CancelSession(id string) error {
	o.sessionsMu.RLock()
	session, ok := o.sessions[id]
	o.sessionsMu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}

	if session.cancel != nil {
		session.cancel()
	}

	session.SetStatus(StatusCancelled)
	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	o.logger.Info("agent session cancelled", "session_id", id)
	return nil
}

// runAgent executes the agent workflow.
func (o *Orchestrator) runAgent(ctx context.Context, session *Session) {
	// Ensure context is cancelled when done (prevents resource leak)
	defer func() {
		if session.cancel != nil {
			session.cancel()
		}
	}()

	o.logger.Info("runAgent: starting agent workflow",
		"session_id", session.ID,
		"issue_number", session.Issue.Number,
		"issue_title", session.Issue.Title)

	session.SetStatus(StatusRunning)

	// Build the system prompt
	o.logger.Debug("runAgent: building system prompt", "session_id", session.ID)
	systemPrompt := o.buildSystemPrompt(session.Issue)
	o.logger.Debug("runAgent: system prompt built",
		"session_id", session.ID,
		"prompt_length", len(systemPrompt))

	// Create branch name (sanitize for git safety)
	branch := sanitizeBranch(fmt.Sprintf("agent/%s", session.ID))
	o.logger.Info("runAgent: created branch name",
		"session_id", session.ID,
		"branch", branch)

	if o.config.OpencodeAddr != "" {
		o.runAgentAPI(ctx, session, systemPrompt, branch)
	} else {
		o.runAgentCLI(ctx, session, systemPrompt, branch)
	}
}

// runAgentCLI executes the agent workflow using the local OpenCode binary.
func (o *Orchestrator) runAgentCLI(ctx context.Context, session *Session, systemPrompt, branch string) {
	// Create session workspace
	workspaceDir := filepath.Join(o.config.WorkingDir, session.ID)
	if err := os.MkdirAll(workspaceDir, 0750); err != nil {
		o.logger.Error("runAgentCLI: failed to create workspace directory", "session_id", session.ID, "dir", workspaceDir, "error", err)
		session.SetStatus(StatusFailed)
		session.SetError(fmt.Sprintf("Failed to create workspace: %v", err))
		return
	}

	// Clone project into workspace
	o.logger.Info("runAgentCLI: preparing workspace", "session_id", session.ID, "dir", workspaceDir)
	if err := o.prepareWorkspace(ctx, workspaceDir); err != nil {
		o.logger.Error("runAgentCLI: failed to prepare workspace", "session_id", session.ID, "error", err)
		session.SetStatus(StatusFailed)
		session.SetError(fmt.Sprintf("Failed to prepare workspace: %v", err))
		return
	}

	// Build OpenCode command
	cmd := o.buildOpenCodeCommand(ctx, session.Issue, systemPrompt, branch)
	cmd.Dir = workspaceDir // Run in the isolated workspace

	o.logger.Info("runAgentCLI: starting OpenCode process",
		"session_id", session.ID,
		"command", cmd.String(),
		"working_dir", cmd.Dir,
		"timeout", o.config.Timeout)

	// Run the agent
	output, err := cmd.CombinedOutput()
	if err != nil {
		session.SetStatus(StatusFailed)
		session.SetError(fmt.Sprintf("Agent failed: %v\nOutput: %s", err, string(output)))
		session.mu.Lock()
		session.CompletedAt = time.Now()
		session.mu.Unlock()
		o.logger.Error("runAgentCLI: agent process failed",
			"session_id", session.ID,
			"error", err,
			"output_length", len(output),
			"output_preview", truncateString(string(output), 500))
		return
	}

	o.logger.Info("runAgentCLI: agent process completed successfully",
		"session_id", session.ID,
		"output_length", len(output))

	// Parse result
	result := o.parseAgentOutput(string(output))

	session.SetResult(result)
	session.SetStatus(StatusCompleted)
	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	o.logger.Info("runAgentCLI: agent session completed successfully",
		"session_id", session.ID,
		"pr_number", result.PRNumber,
		"pr_url", result.PRURL,
		"branch", result.Branch,
		"files_changed", len(result.FilesChanged),
		"iterations", result.Iterations,
		"verdict", result.Verdict,
		"duration", session.CompletedAt.Sub(session.StartedAt))
}

// runAgentAPI executes the agent workflow using the OpenCode HTTP API.
func (o *Orchestrator) runAgentAPI(ctx context.Context, session *Session, systemPrompt, branch string) {
	o.logger.Info("runAgentAPI: starting OpenCode API workflow", "session_id", session.ID, "api_url", o.config.OpencodeAddr)

	client := NewOpenCodeClient(o.config.OpencodeAddr, "", o.logger)

	// 1. Health check
	if err := client.HealthCheck(ctx); err != nil {
		o.logger.Error("runAgentAPI: health check failed", "session_id", session.ID, "error", err)
		o.failSessionf(session, "OpenCode API health check failed: %v", err)
		return
	}

	// 2. Verify MCP server is configured in OpenCode (best effort)
	if err := o.verifyMCPConfig(ctx); err != nil {
		o.logger.Warn("runAgentAPI: MCP server 'code-warden' may not be configured in OpenCode",
			"error", err,
			"hint", "Add MCP server to ~/.config/opencode/opencode.json or run: opencode mcp list")
	}

	// 3. Create session
	title := fmt.Sprintf("Issue %d: %s", session.Issue.Number, session.Issue.Title)
	opencodeSession, err := client.CreateSession(ctx, title, o.config.Model, nil)
	if err != nil {
		o.logger.Error("runAgentAPI: failed to create session", "session_id", session.ID, "error", err)
		o.failSessionf(session, "Failed to create OpenCode session: %v", err)
		return
	}
	o.logger.Info("runAgentAPI: created OpenCode session", "session_id", session.ID, "opencode_session_id", opencodeSession.ID)

	// Ensure session cleanup on error
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.CloseSession(closeCtx, opencodeSession.ID); err != nil {
			o.logger.Warn("runAgentAPI: failed to close OpenCode session", "session_id", opencodeSession.ID, "error", err)
		}
	}()

	// 4. Execute agent workflow
	if !o.executeAgentWorkflow(ctx, session, client, opencodeSession.ID, systemPrompt, branch) {
		return
	}

	// 5. Complete session
	o.completeSession(session, branch, opencodeSession.ID)
}

// failSessionf marks a session as failed with the given error message.
func (o *Orchestrator) failSessionf(session *Session, format string, args ...interface{}) {
	session.SetStatus(StatusFailed)
	session.SetError(fmt.Sprintf(format, args...))
	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()
}

// executeAgentWorkflow runs the agent workflow steps and returns true if successful.
func (o *Orchestrator) executeAgentWorkflow(ctx context.Context, session *Session, client *OpenCodeClient, sessionID, systemPrompt, branch string) bool {
	// Create branch
	if err := client.CreateBranch(ctx, sessionID, branch); err != nil {
		o.logger.Warn("runAgentAPI: failed to create branch via shell command", "error", err)
	}

	// Send message to agent
	if _, err := client.SendMessage(ctx, sessionID, systemPrompt, nil); err != nil {
		o.failSessionf(session, "Failed to send message: %v", err)
		o.logger.Error("runAgentAPI: failed to send message", "session_id", session.ID, "error", err)
		return false
	}
	o.logger.Info("runAgentAPI: message execution completed", "session_id", session.ID)

	// Check session status
	sessInfo, err := client.GetSession(ctx, sessionID)
	if err == nil && sessInfo.Status == "failed" {
		o.failSessionf(session, "OpenCode session failed remotely")
		return false
	}

	return true
}

// completeSession finalizes a successful agent session.
func (o *Orchestrator) completeSession(session *Session, branch, opencodeSessionID string) {
	result := o.parseAgentOutput("API session completed")
	result.Branch = branch

	session.SetResult(result)
	session.SetStatus(StatusCompleted)
	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	o.logger.Info("runAgentAPI: agent session completed successfully",
		"session_id", session.ID,
		"opencode_session_id", opencodeSessionID,
		"duration", session.CompletedAt.Sub(session.StartedAt))
}

// buildSystemPrompt creates the system prompt for the agent.
func (o *Orchestrator) buildSystemPrompt(issue Issue) string {
	return fmt.Sprintf(`You are an autonomous coding agent working on the code-warden project.

## Your Tools
- MCP server available at %s with these tools:
  - search_code(query, limit, chunk_type) - Find relevant code in the codebase
  - get_arch_context(directory) - Get architectural summary for a directory
  - get_symbol(name) - Get type/function definition
  - get_structure() - Get project structure
  - review_code(diff) - Request internal code review

## Your Task
Implement the issue described below. Follow these steps IN ORDER:

1. **Understand** - Read the issue carefully
2. **Explore** - Use MCP tools to understand the codebase
3. **Plan** - Identify files to modify and changes needed
4. **Implement** - Write the code
5. **STOP AND VERIFY** - You MUST run these commands before proceeding:
   - Run: make lint
   - Run: make test
   - If EITHER command fails, you MUST fix the issues and run BOTH commands again
   - Only proceed when BOTH commands pass with exit code 0
6. **Review** - Call review_code on your changes
7. **Iterate** - If REQUEST_CHANGES, fix issues, run lint/test again, and review again
8. **Submit** - Create a pull request ONLY after steps 5-7 are complete

## MANDATORY REQUIREMENTS (DO NOT SKIP):
1. You MUST run 'make lint' and it MUST pass (exit code 0)
2. You MUST run 'make test' and it MUST pass (exit code 0)
3. You MUST call review_code tool for code review
4. You MUST NOT create a PR until all above steps pass

If you cannot complete any step, report what failed and why.

## Issue #%d: %s

%s

## Additional Instructions
%s

## MCP Server
Connect to the MCP server at %s to access project context.

## Working Directory
Work in the current isolated workspace. Create a branch named 'agent/%s' for your changes.
`,
		o.config.MCPAddr,
		issue.Number,
		issue.Title,
		issue.Body,
		issue.Instructions,
		o.config.MCPAddr,
		sessionIDFromIssue(issue),
	)
}

// buildOpenCodeCommand creates the command to run OpenCode.
func (o *Orchestrator) buildOpenCodeCommand(ctx context.Context, issue Issue, systemPrompt, branch string) *exec.Cmd {
	// OpenCode CLI usage: opencode run [message..]
	// The prompt is passed as positional arguments after "run"
	//nolint:gosec // G204: Subprocess launched with variable arguments - intentional for agent execution
	cmd := exec.CommandContext(ctx, "opencode",
		"run",
		"--model", o.config.Model,
		"--agent", "build",
		systemPrompt,
	)

	// Command construction (cmd.Dir is set by the caller)

	// Dynamically configure MCP server for this run
	mcpConfig := fmt.Sprintf(`{"mcp": {"code-warden": {"type": "remote", "url": "http://%s/sse", "enabled": true}}}`, o.config.MCPAddr)

	// Set environment variables for MCP and iteration config
	cmd.Env = append(os.Environ(),
		"OPENCODE_CONFIG_CONTENT="+mcpConfig,
		"OPENCODE_MCP_SERVER="+o.config.MCPAddr,
		"OPENCODE_MAX_ITERATIONS="+fmt.Sprintf("%d", o.config.MaxIterations),
		"OPENCODE_BRANCH="+branch,
		"OPENCODE_REPO_OWNER="+issue.RepoOwner,
		"OPENCODE_REPO_NAME="+issue.RepoName,
		"OPENCODE_ISSUE_NUMBER="+fmt.Sprintf("%d", issue.Number),
	)

	return cmd
}

// parseAgentOutput extracts the result from agent output.
// prepareWorkspace clones the project into the isolated workspace.
func (o *Orchestrator) prepareWorkspace(ctx context.Context, destDir string) error {
	// Simple approach: rsync or cp -r, but git clone is better for a clean state
	// Since we are already in a git repo, we can clone from o.projectRoot
	//nolint:gosec // G204: Subprocess launched with variable arguments - intentional for workspace preparation
	cmd := exec.CommandContext(ctx, "git", "clone", o.projectRoot, ".")
	cmd.Dir = destDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %w (output: %s)", err, string(output))
	}
	return nil
}

// TODO: Implement actual parsing when OpenCode output format is defined.
func (o *Orchestrator) parseAgentOutput(_ string) *Result {
	// TODO: Parse structured output from OpenCode
	// This will depend on the actual OpenCode output format
	o.logger.Debug("parseAgentOutput: parsing agent output (placeholder)")
	return &Result{
		PRNumber:     0,
		Branch:       "",
		FilesChanged: []string{},
		Verdict:      "UNKNOWN",
		Iterations:   1,
	}
}

// generateSessionID creates a unique session ID using cryptographic random.
func generateSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("agent-%x", b)
}

func sessionIDFromIssue(issue Issue) string {
	return fmt.Sprintf("issue-%d-%d", issue.Number, time.Now().Unix())
}

// truncateString truncates a string to maxLen characters.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// sanitizeBranch sanitizes a string to be a valid git branch name.
// Git branch names cannot contain spaces, ~, ^, :, *, ?, [, \\, or start with -.
func sanitizeBranch(name string) string {
	// Replace invalid characters with hyphens
	re := regexp.MustCompile(`[\s~^:?*\[\\]`)
	sanitized := re.ReplaceAllString(name, "-")

	// Remove leading/trailing hyphens and dots
	for len(sanitized) > 0 && (sanitized[0] == '-' || sanitized[0] == '.') {
		sanitized = sanitized[1:]
	}
	for len(sanitized) > 0 && (sanitized[len(sanitized)-1] == '-' || sanitized[len(sanitized)-1] == '.') {
		sanitized = sanitized[:len(sanitized)-1]
	}

	// Limit length to 200 characters
	if len(sanitized) > 200 {
		sanitized = sanitized[:200]
	}

	return sanitized
}

// verifyMCPConfig checks if the code-warden MCP server is configured in OpenCode.
// It returns an error if the MCP server is not found or not properly configured.
func (o *Orchestrator) verifyMCPConfig(ctx context.Context) error {
	// Try to check MCP server status via OpenCode's health/list endpoints
	// This is a best-effort check - the actual verification happens when OpenCode
	// tries to use the MCP tools.

	// Check if our MCP server is reachable
	mcpURL := fmt.Sprintf("http://%s/sse", o.config.MCPAddr)

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mcpURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create MCP health check request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("MCP server not reachable at %s: %w", mcpURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MCP server returned status %d", resp.StatusCode)
	}

	o.logger.Info("MCP server health check passed", "url", mcpURL)
	return nil
}
