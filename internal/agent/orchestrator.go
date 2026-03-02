// Package agent provides orchestration for AI coding agents.
// It manages agent sessions, spawns OpenCode processes, and handles the
// communication between code-warden and the agent.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
)

// Orchestrator manages agent sessions and their lifecycle.
type Orchestrator struct {
	ghClient    github.Client
	mcpServer   *mcp.Server
	httpServer  *http.Server
	logger      *slog.Logger
	config      Config
	projectRoot string // Path to the repository being worked on
	repoConfig  *core.RepoConfig
	repo        *storage.Repository

	sessions   map[string]*Session
	sessionsMu sync.RWMutex

	// done is closed when the orchestrator is shutting down.
	done chan struct{}
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

	// MaxConcurrentSessions is the maximum number of concurrent agent sessions.
	MaxConcurrentSessions int `yaml:"max_concurrent_sessions"`

	// MCPAddr is the address for the MCP server.
	MCPAddr string `yaml:"mcp_addr"`

	// WorkingDir is the directory for agent workspaces.
	WorkingDir string `yaml:"working_dir"`

	// ComparisonModels are models for consensus review (optional).
	ComparisonModels []string `yaml:"comparison_models"`

	// ReviewsDir is the directory to save review artifacts (optional).
	ReviewsDir string `yaml:"reviews_dir"`

	// MCPTimeout is the timeout for individual MCP tool calls.
	// Used to configure OpenCode to wait longer for slow tool responses like code review.
	MCPTimeout time.Duration `yaml:"mcp_timeout"`
}

// DefaultConfig returns default configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:               false,
		Provider:              "opencode",
		Model:                 "llama3.1:70b",
		Timeout:               30 * time.Minute,
		MaxIterations:         3,
		MaxConcurrentSessions: 3,
		MCPAddr:               "127.0.0.1:8081",
		MCPTimeout:            5 * time.Minute, // Default 5 minutes for MCP tool calls
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

	return &Orchestrator{
		ghClient:    ghClient,
		mcpServer:   mcpServer,
		logger:      logger,
		config:      config,
		projectRoot: absRoot,
		repoConfig:  repoConfig,
		repo:        repo,
		sessions:    make(map[string]*Session),
		sessionsMu:  sync.RWMutex{},
		done:        make(chan struct{}),
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
			"provider", o.config.Provider,
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
				o.cleanupWorkspace(id)
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

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	session.cancel = cancel

	// Start agent in background
	go o.runAgent(ctx, session)

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

	// Create branch name (sanitize for git safety)
	branch := gitutil.SanitizeBranch(fmt.Sprintf("agent/%s", session.ID))
	o.logger.Info("runAgent: created branch name",
		"session_id", session.ID,
		"branch", branch)

	// Build the system prompt
	o.logger.Debug("runAgent: building system prompt", "session_id", session.ID)
	systemPrompt := o.buildSystemPrompt(session.Issue, branch)
	o.logger.Debug("runAgent: system prompt built",
		"session_id", session.ID,
		"prompt_length", len(systemPrompt))

	o.runAgentCLI(ctx, session, systemPrompt, branch)
}

// runAgentCLI executes the agent workflow using the local OpenCode binary.
func (o *Orchestrator) runAgentCLI(ctx context.Context, session *Session, systemPrompt, branch string) {
	defer o.cleanupWorkspace(session.ID)

	ws, err := o.prepareAgentWorkspace(ctx, session)
	if err != nil {
		o.logger.Error("runAgentCLI: workspace setup failed", "session_id", session.ID, "error", err)
		session.SetStatus(StatusFailed)
		session.SetError(err.Error())
		return
	}
	defer ws.logFile.Close()
	defer o.mcpServer.UnregisterWorkspace(session.ID)

	cmd, cleanup := o.buildOpenCodeCommand(ctx, session.Issue, systemPrompt, branch)
	defer cleanup()
	cmd.Dir = ws.dir
	cmd.Stdout = ws.logFile
	cmd.Stderr = ws.logFile

	o.logger.Info("runAgentCLI: starting OpenCode process",
		"session_id", session.ID,
		"command", cmd.String(),
		"working_dir", cmd.Dir,
		"log_file", ws.logPath,
		"timeout", o.config.Timeout)

	runErr := cmd.Run()

	// Read log file (capped at 10MB, from the end to capture AGENT_RESULT sentinel).
	outputBytes, readErr := o.readLogFile(ws.logPath, 10*1024*1024)
	if readErr != nil {
		o.logger.Warn("runAgentCLI: failed to read log file", "session_id", session.ID, "path", ws.logPath, "error", readErr)
	}
	output := string(outputBytes)

	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	if runErr != nil {
		session.SetStatus(StatusFailed)
		// truncateTail captures the most recent (failure-related) output for the error message.
		// truncateString captures the start of output for the log preview.
		session.SetError(fmt.Sprintf("Agent failed: %v\nTail of output:\n%s", runErr, truncateTail(output, 2000)))
		o.logger.Error("runAgentCLI: agent process failed",
			"session_id", session.ID,
			"error", runErr,
			"log_file", ws.logPath,
			"output_length", len(output),
			"output_preview", truncateString(output, 500))
		return
	}

	o.logger.Info("runAgentCLI: agent process completed successfully",
		"session_id", session.ID,
		"log_file", ws.logPath,
		"output_length", len(output))

	result := o.parseAgentOutput(output, branch)
	session.SetResult(result)
	session.SetStatus(StatusCompleted)

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

// buildSystemPrompt creates the system prompt for the agent.
//
//nolint:funlen // System prompt construction is inherently long
func (o *Orchestrator) buildSystemPrompt(issue Issue, branch string) string {
	// Get verify commands from repo config, or use defaults
	verifyCmds := o.getVerifyCommands()
	verifyCmdList := strings.Join(verifyCmds, "\n   - Run: ")
	verifyCmdCheck := strings.Join(verifyCmds, " && ")

	customInstructions := "None provided."
	if o.repoConfig != nil && len(o.repoConfig.CustomInstructions) > 0 {
		customInstructions = strings.Join(o.repoConfig.CustomInstructions, "\n")
	}

	projectContext := "No project context generated yet. Please explore the repository using MCP tools."
	if o.repo != nil && o.repo.GeneratedContext != "" {
		projectContext = "The following architectural and contextual document was automatically generated by analyzing the entire repository:\n" + o.repo.GeneratedContext
	}

	return fmt.Sprintf(`You are an autonomous coding agent working on the %s project.

## Your Tools
- MCP server available at %s with these tools:
  - search_code(query, limit, chunk_type) - Find relevant code in the codebase
  - get_arch_context(directory) - Get architectural summary for a directory
  - get_symbol(name) - Get type/function definition
  - get_structure() - Get project structure
  - review_code(diff) - Request internal code review
  - push_branch(branch) - Push local branch to remote (REQUIRED before PR)
  - create_pull_request(title, body, head, base) - Create a GitHub PR (ONLY after push_branch)
  - list_issues(state, labels) - List repository issues
  - get_issue(number) - Get issue details

## Your Task
Implement the issue described below. Follow these steps IN ORDER:

1. **Understand** - Read the issue carefully
2. **Explore** - Use MCP tools to understand the codebase
3. **Plan** - Identify files to modify and changes needed
4. **Implement** - Write the code
5. **STOP AND VERIFY** - You MUST run these commands before proceeding:
   - Run: %s
   - If ANY command fails, you MUST fix the issues and run ALL commands again
   - Only proceed when ALL commands pass with exit code 0
6. **Review** - Call review_code on your changes
   - CRITICAL: At the start of each review cycle, you MUST print 'AGENT_ITERATION: X' (where X is the current iteration number) on its own line.
   - CRITICAL: Check the verdict from review_code. If verdict is "APPROVE", proceed to step 8. If verdict is "REQUEST_CHANGES", proceed to step 7.
7. **Iterate** - If REQUEST_CHANGES:
   - Fix all issues identified in the review
   - Run: %s (all must pass)
   - Call review_code again
   - Repeat until you receive APPROVE verdict (max 3 iterations)
8. **Push** - Run: git push origin HEAD (or use push_branch tool)
   - CRITICAL: Your branch MUST exist on GitHub before creating a PR
   - If push_branch tool is not available, run: git push origin <branch-name>
9. **Submit** - Call create_pull_request ONLY after receiving APPROVE verdict

## MANDATORY REQUIREMENTS (DO NOT SKIP):
1. You MUST run all verification commands and they MUST pass (exit code 0)
2. You MUST call review_code tool for code review
3. You MUST receive "APPROVE" verdict from review_code before creating a PR
4. You MUST push your branch to GitHub BEFORE calling create_pull_request
5. You MUST NOT create a PR until ALL above steps are complete

## CRITICAL: Branch Push Requirement
The create_pull_request tool will FAIL with "422 Validation Failed" if the branch does not exist on GitHub.
You MUST call push_branch(branch_name) BEFORE create_pull_request.
Example sequence:
  1. push_branch("agent/issue-123")  <- Push to GitHub
  2. create_pull_request(...)         <- Only after push succeeds

## CRITICAL: Review Approval Requirement
The review_code tool returns a JSON response with a "verdict" field.
Possible verdict values:
  - "APPROVE" - Code is approved, proceed to push and create PR
  - "REQUEST_CHANGES" - Issues found, fix them and review again
  - "COMMENT" - General feedback, treat as REQUEST_CHANGES
You MUST wait for "APPROVE" verdict before creating a PR. Never skip this check.

If you cannot complete any step, report what failed and why.
At the end of your run, you MUST print exactly one line in this format:
AGENT_RESULT: {"pr_number": <n>, "pr_url": "<url>", "branch": "<branch>", "files_changed": ["<file>", ...], "verdict": "<APPROVED|REQUEST_CHANGES|UNKNOWN>", "iterations": <n>}
This line must be the last line of your output.

## Issue #%d: %s

%s

%s

## Project Context & Architecture
%s

## Project Custom Instructions
%s

## MCP Server
Connect to the MCP server at %s to access project context.

## Working Directory
Work in the current isolated workspace. Your changes MUST be in the branch named '%s'.
`,
		issue.RepoOwner+"/"+issue.RepoName,
		o.config.MCPAddr,
		verifyCmdList,
		verifyCmdCheck,
		issue.Number,
		issue.Title,
		issue.Body,
		issue.Instructions,
		projectContext,
		customInstructions,
		o.config.MCPAddr,
		branch,
	)
}

// getVerifyCommands returns the verification commands from repo config or defaults.
func (o *Orchestrator) getVerifyCommands() []string {
	if o.repoConfig != nil && len(o.repoConfig.VerifyCommands) > 0 {
		return o.repoConfig.VerifyCommands
	}
	// Default commands for Go projects
	return []string{"make lint", "make test"}
}

// buildOpenCodeCommand creates the command to run OpenCode and returns a cleanup function.
func (o *Orchestrator) buildOpenCodeCommand(ctx context.Context, issue Issue, systemPrompt, branch string) (*exec.Cmd, func()) {
	// OpenCode CLI usage: opencode run [message..]
	// The prompt is passed as positional arguments after "run"
	//nolint:gosec // G204: Subprocess launched with variable arguments - intentional for agent execution
	cmd := exec.CommandContext(ctx, "opencode",
		"run",
		"--model", o.config.Model,
		"--agent", "build",
		systemPrompt,
	)

	// Create OpenCode config with MCP timeout
	configPath, err := o.createOpenCodeConfig()
	cleanup := func() {
		if configPath != "" {
			_ = os.Remove(configPath)
		}
	}

	if err != nil {
		o.logger.Warn("failed to create OpenCode config, using defaults", "error", err)
	}

	// Set environment variables for MCP and iteration config
	env := os.Environ()
	env = append(env,
		"OPENCODE_MAX_ITERATIONS="+fmt.Sprintf("%d", o.config.MaxIterations),
		"OPENCODE_BRANCH="+branch,
		"OPENCODE_REPO_OWNER="+issue.RepoOwner,
		"OPENCODE_REPO_NAME="+issue.RepoName,
		"OPENCODE_ISSUE_NUMBER="+fmt.Sprintf("%d", issue.Number),
	)
	if configPath != "" {
		env = append(env, "OPENCODE_CONFIG="+configPath)
	}
	cmd.Env = env

	return cmd, cleanup
}

// createOpenCodeConfig creates a temporary OpenCode config file with MCP timeout settings.
func (o *Orchestrator) createOpenCodeConfig() (string, error) {
	// OpenCode expects timeout in milliseconds
	timeoutMs := o.config.MCPTimeout.Milliseconds()

	config := map[string]any{
		"experimental": map[string]any{
			"mcp_timeout": timeoutMs,
		},
	}

	configJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal OpenCode config: %w", err)
	}

	// Create temp file
	tmpFile, err := os.CreateTemp("", "opencode-config-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create temp config file: %w", err)
	}

	if _, err := tmpFile.Write(configJSON); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write config file: %w", err)
	}
	_ = tmpFile.Close()

	o.logger.Debug("created OpenCode config", "path", tmpFile.Name(), "mcp_timeout_ms", timeoutMs)
	return tmpFile.Name(), nil
}

// parseAgentOutput extracts the result from agent output.
func (o *Orchestrator) parseAgentOutput(output string, sessionBranch string) *Result {
	const resultSentinel = "AGENT_RESULT:"
	const iterationSentinel = "AGENT_ITERATION:"
	lines := strings.Split(output, "\n")

	// scan for the final result JSON.
	var finalResult *Result
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, resultSentinel) {
			jsonStr := strings.TrimPrefix(line, resultSentinel)
			jsonStr = strings.TrimSpace(jsonStr)

			var res Result
			if err := json.Unmarshal([]byte(jsonStr), &res); err != nil {
				o.logger.Warn("parseAgentOutput: failed to unmarshal agent result", "error", err, "line", line)
				continue
			}
			if res.FilesChanged == nil {
				res.FilesChanged = []string{}
			}
			o.logger.Debug("parseAgentOutput: successfully parsed agent result", "pr_number", res.PRNumber)
			finalResult = &res
			break // Use the first valid AGENT_RESULT found
		}
	}

	if finalResult != nil {
		return finalResult
	}

	// fallback: No final result found, infer from logs.
	o.logger.Warn("parseAgentOutput: no AGENT_RESULT sentinel found, attempting to infer result")
	res := &Result{
		Branch:       sessionBranch,
		FilesChanged: []string{}, // Consistency: always empty slice, never nil
		Verdict:      "UNKNOWN",
		Iterations:   1, // Initialise to 1; will be incremented for each visible sentinel
	}

	foundIterations := 0
	for _, line := range lines {
		if strings.Contains(line, iterationSentinel) {
			foundIterations++
		}
	}

	if foundIterations > 0 {
		res.Iterations = foundIterations
	}

	return res
}
