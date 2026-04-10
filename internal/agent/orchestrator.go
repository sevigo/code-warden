// Package agent provides orchestration for AI coding agents.
// It manages agent sessions and runs in-process agent loops (native/warden modes).
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/sevigo/goframe/llms"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/globalmcp"
	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
)

// Orchestrator manages agent sessions and their lifecycle.
type Orchestrator struct {
	ghClient          github.Client
	mcpServer         *mcp.Server
	globalMCPRegistry *globalmcp.WorkspaceRegistry
	httpServer        *http.Server
	logger            *slog.Logger
	config            Config
	projectRoot       string
	repoConfig        *core.RepoConfig
	repo              *storage.Repository
	ragService        rag.Service
	llm               llms.Model // used by native in-process agent mode
	store             storage.AgentSessionStore

	sessions   map[string]*Session
	sessionsMu sync.RWMutex

	done chan struct{}
}

// Config holds configuration for the agent orchestrator.
type Config struct {
	Enabled               bool          `yaml:"enabled"`
	Mode                  string        `yaml:"mode"`
	Model                 string        `yaml:"model"`
	Timeout               time.Duration `yaml:"timeout"`
	MaxIterations         int           `yaml:"max_iterations"`
	MaxConcurrentSessions int           `yaml:"max_concurrent_sessions"`
	MCPAddr               string        `yaml:"mcp_addr"`
	WorkingDir            string        `yaml:"working_dir"`
	ComparisonModels      []string      `yaml:"comparison_models"`
	ReviewsDir            string        `yaml:"reviews_dir"`
	MCPTimeout            time.Duration `yaml:"mcp_timeout"`
	InProcessOnly         bool          `yaml:"in_process_only"`
	BaseBranch            string        `yaml:"base_branch"`
}

// Constants for agent orchestration
const (
	// MaxTitleLength is the maximum length for session titles
	MaxTitleLength = 50
	// DefaultReviewScore is the default score for review results
	DefaultReviewScore = 80.0
)

// DefaultConfig returns default configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:               false,
		Mode:                  "warden",
		Model:                 "qwen2.5-coder",
		Timeout:               30 * time.Minute,
		MaxIterations:         3,
		MaxConcurrentSessions: 3,
		MCPAddr:               "127.0.0.1:8081",
		MCPTimeout:            5 * time.Minute,
		WorkingDir:            "/tmp/code-warden-agents",
	}
}

// NewOrchestrator creates a new agent orchestrator.
func NewOrchestrator(
	store storage.Store,
	vectorStore storage.ScopedVectorStore,
	ragService rag.Service,
	ghClient github.Client,
	ghToken string,
	repo *storage.Repository,
	repoConfig *core.RepoConfig,
	projectRoot string,
	config Config,
	logger *slog.Logger,
	globalMCPRegistry *globalmcp.WorkspaceRegistry,
) *Orchestrator {
	// Create MCP server
	mcpServer := mcp.NewServer(
		store,
		vectorStore,
		ragService,
		ghClient,
		ghToken,
		repo,
		repoConfig,
		projectRoot,
		logger,
		mcp.Config{
			ComparisonModels: config.ComparisonModels,
			ReviewsDir:       config.ReviewsDir,
			AgentMode:        true,
		},
	)

	// Log configuration
	if len(config.ComparisonModels) > 0 {
		logger.Info("MCP server configured for consensus review", "models", config.ComparisonModels)
	} else {
		logger.Info("MCP server configured for single-model review (faster for agent iterations)")
	}

	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		logger.Error("NewOrchestrator: failed to resolve absolute path for projectRoot", "projectRoot", projectRoot, "error", err)
		absRoot = projectRoot
	}

	o := &Orchestrator{
		ghClient:          ghClient,
		mcpServer:         mcpServer,
		globalMCPRegistry: globalMCPRegistry,
		logger:            logger,
		config:            config,
		projectRoot:       absRoot,
		repoConfig:        repoConfig,
		repo:              repo,
		ragService:        ragService,
		store:             store,
		sessions:          make(map[string]*Session),
		sessionsMu:        sync.RWMutex{},
		done:              make(chan struct{}),
	}
	// Pre-populate the review LLM when available; nil-safe — ragService may be
	// absent in tests or when the RAG pipeline is disabled.
	if ragService != nil {
		o.llm = ragService.GeneratorLLM()
	}
	return o
}

// Start begins the MCP HTTP server. Must be called before agents can use tools.
// In native in-process mode (InProcessOnly=true), the HTTP server is skipped
// because tools are injected directly into the goframe registry and never called
// over HTTP.
func (o *Orchestrator) Start() error {
	if !o.config.Enabled {
		o.logger.Info("agent orchestrator is disabled, not starting MCP server")
		return nil
	}

	if o.config.InProcessOnly {
		o.logger.Info("agent orchestrator: in-process-only mode, skipping MCP HTTP server",
			"mode", o.config.Mode)
		go o.cleanupLoop()
		return nil
	}

	o.httpServer = &http.Server{
		Addr:              o.config.MCPAddr,
		Handler:           o.mcpServer,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Minute, // Sufficient for long-lived agent tasks
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
			"model", o.config.Model)

		if err := o.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			o.logger.Error("MCP HTTP server failed", "error", err, "addr", o.config.MCPAddr)
		}
	}()

	// Wait for the server to be ready (with timeout)
	select {
	case <-ready:
		o.logger.Info("MCP HTTP server is ready", "addr", o.config.MCPAddr)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout waiting for MCP server to start on %s", o.config.MCPAddr)
	}

	// Start session cleanup goroutine
	go o.cleanupLoop()

	return nil
}

// Shutdown gracefully stops the MCP server and cleans up resources.
func (o *Orchestrator) Shutdown(ctx context.Context) error {
	o.logger.Info("shutting down agent orchestrator")

	// Cancel all running sessions
	close(o.done) // Signal cleanupLoop to stop
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

	for {
		select {
		case <-o.done:
			return
		case <-ticker.C:
			o.cleanupOldSessions()
		}
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
				// Clean up workspace directory
				if err := o.cleanupWorkspace(id); err != nil {
					o.logger.Warn("cleanup old session failed", "session_id", id, "error", err)
				}
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

// Result represents the result of an agent session.
type Result struct {
	PRNumber      int      `json:"pr_number"`
	PRURL         string   `json:"pr_url"`
	Branch        string   `json:"branch"`
	FilesChanged  []string `json:"files_changed"`
	ReviewSummary string   `json:"review_summary"`
	Verdict       string   `json:"verdict"`
	Iterations    int      `json:"iterations"`
	TokensInput   int64    `json:"tokens_input"`
	TokensOutput  int64    `json:"tokens_output"`
}

// SpawnAgent creates a new agent session to implement an issue.
func (o *Orchestrator) SpawnAgent(ctx context.Context, issue Issue) (*Session, error) {
	if !o.config.Enabled {
		return nil, fmt.Errorf("agent functionality is disabled")
	}

	// Check concurrent session limit and insert atomically
	o.sessionsMu.Lock()
	activeCount := len(o.sessions)
	if activeCount >= o.config.MaxConcurrentSessions {
		o.sessionsMu.Unlock()
		return nil, fmt.Errorf("maximum concurrent sessions reached (%d), please retry later", o.config.MaxConcurrentSessions)
	}

	sessionID := generateSessionID()
	session := &Session{
		ID:        sessionID,
		Issue:     issue,
		status:    StatusPending,
		StartedAt: time.Now(),
	}
	o.sessions[sessionID] = session
	o.sessionsMu.Unlock()

	// Persist the new session row so it's visible immediately.
	o.persistSessionCreated(ctx, session)

	// Create context with timeout
	//nolint:gosec // G118: cancel stored in session for cleanup in runAgentCLI/runAgentSDK
	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	session.mu.Lock()
	session.cancel = cancel
	session.mu.Unlock()

	// Start agent in background
	go o.runAgent(ctx, session)

	// Register workspace with global MCP registry
	if o.globalMCPRegistry != nil {
		mcpEndpoint := fmt.Sprintf("http://%s", o.config.MCPAddr)
		branch := gitutil.SanitizeBranch(fmt.Sprintf("agent/%s", sessionID))
		token, err := o.globalMCPRegistry.RegisterWorkspace(
			mcpEndpoint,
			o.repo.FullName,
			sessionID,
			o.projectRoot,
			globalmcp.WorkspaceMeta{
				Branch:      branch,
				IssueNumber: issue.Number,
			},
		)
		if err != nil {
			o.logger.Error("failed to register workspace with global MCP registry",
				"session_id", sessionID, "error", err)
		} else {
			tokenDisplay := token
			if len(token) > 8 {
				tokenDisplay = token[:8] + "..."
			}
			o.logger.Info("registered workspace with global MCP registry",
				"session_id", sessionID, "token", tokenDisplay, "mcp_endpoint", mcpEndpoint)
		}
	}

	o.logger.Info("agent session started",
		"session_id", sessionID,
		"issue", issue.Number,
		"repo", issue.RepoOwner+"/"+issue.RepoName,
		"active_sessions", activeCount+1)

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

	session.mu.Lock()
	if session.cancel != nil {
		session.cancel()
	}
	session.mu.Unlock()

	session.SetStatus(StatusCancelled)
	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	o.logger.Info("agent session cancelled", "session_id", id)
	return nil
}

// runAgent executes the agent workflow.
func (o *Orchestrator) runAgent(ctx context.Context, session *Session) {
	defer func() {
		if session.cancel != nil {
			session.cancel()
		}
	}()

	o.logger.Info("runAgent: starting agent workflow",
		"session_id", session.ID,
		"issue_number", session.Issue.Number,
		"issue_title", session.Issue.Title,
		"mode", o.config.Mode)

	session.SetStatus(StatusRunning)
	o.postSessionStarted(ctx, session)

	branch := gitutil.SanitizeBranch(fmt.Sprintf("agent/%s", session.ID))
	o.logger.Info("runAgent: created branch name",
		"session_id", session.ID,
		"branch", branch)

	switch o.config.Mode {
	case "native":
		o.runInProcessAgent(ctx, session, branch)
	case "pi", "warden":
		o.runWardenAgent(ctx, session, branch)
	default:
		o.logger.Error("runAgent: unsupported agent mode", "mode", o.config.Mode)
		o.failSession(ctx, session, fmt.Sprintf("unsupported agent mode: %s (use 'native' or 'warden')", o.config.Mode))
	}
}

// extractPRInfo extracts PR number and URL from implementation response.
func extractPRInfo(implementation string) *struct {
	PRNumber int
	PRURL    string
} {
	prPattern := `https://github\.com/[^/]+/[^/]+/pull/(\d+)`
	re := regexp.MustCompile(prPattern)

	if match := re.FindStringSubmatch(implementation); match != nil {
		prNumber, err := strconv.Atoi(match[1])
		if err == nil {
			return &struct {
				PRNumber int
				PRURL    string
			}{
				PRNumber: prNumber,
				PRURL:    match[0],
			}
		}
	}
	return nil
}
