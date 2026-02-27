// Package agent provides orchestration for AI coding agents.
// It manages agent sessions, spawns OpenCode processes, and handles the
// communication between code-warden and the agent.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	logger      *slog.Logger
	config      Config

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
		MCPAddr:       ":8081",
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
		sessions:    make(map[string]*Session),
	}
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
	ID          string
	Issue       Issue
	Status      SessionStatus
	StartedAt   time.Time
	CompletedAt time.Time
	Result      *Result
	Error       string
	cancel      context.CancelFunc
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
		Status:    StatusPending,
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

	session.Status = StatusCancelled
	session.CompletedAt = time.Now()

	o.logger.Info("agent session cancelled", "session_id", id)
	return nil
}

// runAgent executes the agent workflow.
func (o *Orchestrator) runAgent(ctx context.Context, session *Session) {
	o.logger.Info("runAgent: starting agent workflow",
		"session_id", session.ID,
		"issue_number", session.Issue.Number,
		"issue_title", session.Issue.Title)

	session.Status = StatusRunning

	// Build the system prompt
	o.logger.Debug("runAgent: building system prompt", "session_id", session.ID)
	systemPrompt := o.buildSystemPrompt(session.Issue)
	o.logger.Debug("runAgent: system prompt built",
		"session_id", session.ID,
		"prompt_length", len(systemPrompt))

	// Create branch name
	branch := fmt.Sprintf("agent/%s", session.ID)
	o.logger.Info("runAgent: created branch name",
		"session_id", session.ID,
		"branch", branch)

	// Build OpenCode command
	cmd := o.buildOpenCodeCommand(ctx, session.Issue, systemPrompt, branch)

	o.logger.Info("runAgent: starting OpenCode process",
		"session_id", session.ID,
		"command", cmd.String(),
		"working_dir", cmd.Dir,
		"timeout", o.config.Timeout)

	// Run the agent
	output, err := cmd.CombinedOutput()
	if err != nil {
		session.Status = StatusFailed
		session.Error = fmt.Sprintf("Agent failed: %v\nOutput: %s", err, string(output))
		session.CompletedAt = time.Now()
		o.logger.Error("runAgent: agent process failed",
			"session_id", session.ID,
			"error", err,
			"output_length", len(output),
			"output_preview", truncateString(string(output), 500))
		return
	}

	o.logger.Info("runAgent: agent process completed successfully",
		"session_id", session.ID,
		"output_length", len(output))

	// Parse result
	result := o.parseAgentOutput(string(output))

	session.Result = result
	session.Status = StatusCompleted
	session.CompletedAt = time.Now()

	o.logger.Info("runAgent: agent session completed successfully",
		"session_id", session.ID,
		"pr_number", result.PRNumber,
		"pr_url", result.PRURL,
		"branch", result.Branch,
		"files_changed", len(result.FilesChanged),
		"iterations", result.Iterations,
		"verdict", result.Verdict,
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
Implement the issue described below. Follow these steps:

1. **Understand** - Read the issue carefully
2. **Explore** - Use MCP tools to understand the codebase
3. **Plan** - Identify files to modify and changes needed
4. **Implement** - Write the code
5. **Review** - Call review_code on your changes
6. **Iterate** - If REQUEST_CHANGES, fix issues and review again
7. **Submit** - Create a pull request when APPROVED

## Issue #%d: %s

%s

## Additional Instructions
%s

## MCP Server
Connect to the MCP server at %s to access project context.

## Working Directory
Work in the current directory. Create a branch named 'agent/%s' for your changes.
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
func (o *Orchestrator) buildOpenCodeCommand(ctx context.Context, _ Issue, systemPrompt, branch string) *exec.Cmd {
	// OpenCode command structure (headless mode with Ollama)
	// This is a placeholder - adjust based on actual OpenCode CLI
	//nolint:gosec // G204: Subprocess launched with variable arguments - intentional for agent execution
	cmd := exec.CommandContext(ctx, "opencode",
		"--headless",
		"--model", o.config.Model,
		"--mcp", o.config.MCPAddr,
		"--branch", branch,
		"--prompt", systemPrompt,
	)

	// Set working directory
	cmd.Dir = o.config.WorkingDir

	// Set environment variables
	cmd.Env = append(os.Environ(),
		"OPENCODE_MCP_SERVER="+o.config.MCPAddr,
		"OPENCODE_MAX_ITERATIONS="+fmt.Sprintf("%d", o.config.MaxIterations),
	)

	return cmd
}

// parseAgentOutput extracts the result from agent output.
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

// generateSessionID creates a unique session ID.
func generateSessionID() string {
	return fmt.Sprintf("agent-%d", time.Now().UnixNano())
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
