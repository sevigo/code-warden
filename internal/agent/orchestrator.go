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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
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

	sessions   map[string]*Session
	sessionsMu sync.RWMutex

	done chan struct{}
}

// Config holds configuration for the agent orchestrator.
type Config struct {
	// Enabled determines if agent functionality is active.
	Enabled bool `yaml:"enabled"`

	// Provider is the agent provider (e.g., "opencode", "goose", "claude").
	Provider string `yaml:"provider"`

	// Mode is how to connect to the agent:
	//   "server"  — goframe/agent SDK connects to the provider's HTTP server (e.g., OpenCode).
	//   "cli"     — spawns the provider's binary as a subprocess.
	//   "native"  — in-process ReAct loop using goframe AgentLoop; no external process needed.
	//               Uses the same LLM configured for code reviews.
	Mode string `yaml:"mode"`

	// Model is the LLM model to use.
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
	// Used to configure the provider to wait longer for slow tool responses.
	MCPTimeout time.Duration `yaml:"mcp_timeout"`

	// OpenCodeURL is the URL of the OpenCode server (required when mode is "server").
	OpenCodeURL string `yaml:"opencode_url"`
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
		Provider:              "opencode",
		Mode:                  "server",
		Model:                 "qwen2.5-coder",
		Timeout:               30 * time.Minute,
		MaxIterations:         3,
		MaxConcurrentSessions: 3,
		MCPAddr:               "127.0.0.1:8081",
		MCPTimeout:            5 * time.Minute,
		WorkingDir:            "/tmp/code-warden-agents",
		OpenCodeURL:           "http://localhost:3000",
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
		ghClient:          ghClient,
		mcpServer:         mcpServer,
		globalMCPRegistry: globalMCPRegistry,
		logger:            logger,
		config:            config,
		projectRoot:       absRoot,
		repoConfig:        repoConfig,
		repo:              repo,
		ragService:        ragService,
		llm:               ragService.GeneratorLLM(),
		sessions:          make(map[string]*Session),
		sessionsMu:        sync.RWMutex{},
		done:              make(chan struct{}),
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
	// Ensure context is cancelled when done (prevents resource leak)
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

	// Create branch name (sanitize for git safety)
	branch := gitutil.SanitizeBranch(fmt.Sprintf("agent/%s", session.ID))
	o.logger.Info("runAgent: created branch name",
		"session_id", session.ID,
		"branch", branch)

	switch o.config.Mode {
	case "server":
		o.runAgentSDK(ctx, session, branch)
	case "native":
		o.runInProcessAgent(ctx, session, branch)
	default:
		// CLI mode: spawn external binary
		o.logger.Info("🧭 EXPLORATION: Building agent context", "session_id", session.ID)
		systemPrompt := o.buildSystemPrompt(session.Issue, branch)
		o.logger.Debug("runAgent: system prompt built",
			"session_id", session.ID,
			"prompt_length", len(systemPrompt))
		o.runAgentCLI(ctx, session, systemPrompt, branch)
	}
}

// runAgentCLI executes the agent workflow using the local OpenCode binary.
func (o *Orchestrator) runAgentCLI(ctx context.Context, session *Session, systemPrompt, branch string) {
	defer func() {
		if err := o.cleanupWorkspace(session.ID); err != nil {
			o.logger.Error("cleanup failed", "session_id", session.ID, "error", err)
		}
		if o.globalMCPRegistry != nil {
			if err := o.globalMCPRegistry.UnregisterWorkspaceBySessionID(session.ID); err != nil {
				o.logger.Warn("failed to unregister workspace from global registry",
					"session_id", session.ID, "error", err)
			}
		}
	}()

	ws, err := o.prepareAgentWorkspace(ctx, session)
	if err != nil {
		o.logger.Error("runAgentCLI: workspace setup failed", "session_id", session.ID, "error", err)
		session.SetStatus(StatusFailed)
		session.SetError(err.Error())
		return
	}
	defer ws.logFile.Close()
	defer o.mcpServer.UnregisterWorkspace(session.ID)

	cmd, cleanup := o.buildOpenCodeCommand(ctx, session.Issue, systemPrompt, branch, session.ID)
	defer cleanup()
	cmd.Dir = ws.dir
	cmd.Stdout = ws.logFile
	cmd.Stderr = ws.logFile

	o.logger.Info("🛠️ IMPLEMENTATION: Starting OpenCode process",
		"session_id", session.ID,
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
		errMsg := fmt.Sprintf("Agent failed: %v\nTail of output:\n%s", runErr, truncateTail(output, 2000))
		session.SetStatus(StatusFailed)
		session.SetError(errMsg)
		o.logger.Error("runAgentCLI: agent process failed",
			"session_id", session.ID,
			"error", runErr,
			"log_file", ws.logPath,
			"output_length", len(output),
			"output_preview", truncateString(output, 500))
		o.postSessionFailed(ctx, session, errMsg)
		return
	}

	o.logger.Info("runAgentCLI: agent process completed successfully",
		"session_id", session.ID,
		"log_file", ws.logPath,
		"output_length", len(output))

	result := o.parseAgentOutput(output, branch)
	session.SetResult(result)
	session.SetStatus(StatusCompleted)
	o.postSessionCompleted(ctx, session, result)

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
   - CRITICAL: Check the verdict from review_code. If verdict is "APPROVE" or "COMMENT", proceed to step 8. If verdict is "REQUEST_CHANGES", proceed to step 7.
7. **Iterate** - If REQUEST_CHANGES:
   - Fix all issues identified in the review
   - Run: %s (all must pass)
   - Call review_code again
   - Repeat until you receive APPROVE or COMMENT verdict (max 3 iterations)
8. **Push** - Run: git push origin HEAD (or use push_branch tool)
   - CRITICAL: Your branch MUST exist on GitHub before creating a PR
   - If push_branch tool is not available, run: git push origin <branch-name>
9. **Submit** - Call create_pull_request ONLY after receiving APPROVE or COMMENT verdict

## MANDATORY REQUIREMENTS (DO NOT SKIP):
1. You MUST run all verification commands and they MUST pass (exit code 0)
2. You MUST call review_code tool for code review
3. You MUST receive "APPROVE" or "COMMENT" verdict from review_code before creating a PR
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
  - "COMMENT" - General feedback without blockers, proceed to push and create PR
  - "REQUEST_CHANGES" - Issues found, fix them and review again
You MUST wait for "APPROVE" or "COMMENT" verdict before creating a PR. Never skip this check.

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
func (o *Orchestrator) buildOpenCodeCommand(ctx context.Context, issue Issue, systemPrompt, branch, sessionID string) (*exec.Cmd, func()) {
	// OpenCode CLI usage: opencode run [message..]
	// The prompt is passed as positional arguments after "run"
	//nolint:gosec // G204: Subprocess launched with variable arguments - intentional for agent execution
	cmd := exec.CommandContext(ctx, "opencode",
		"run",
		"--model", o.config.Model,
		"--agent", "build",
		systemPrompt,
	)

	// Create OpenCode config with MCP server and timeout
	configPath, err := o.createOpenCodeConfig(sessionID)
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

// createOpenCodeConfig creates a temporary OpenCode config file with MCP server and timeout settings.
func (o *Orchestrator) createOpenCodeConfig(sessionID string) (string, error) {
	// OpenCode expects timeout in milliseconds
	timeoutMs := o.config.MCPTimeout.Milliseconds()

	// Build MCP server URL with workspace query parameter for per-session routing
	mcpURL := fmt.Sprintf("http://%s/sse?workspace=%s", o.config.MCPAddr, sessionID)

	config := map[string]any{
		"mcp": map[string]any{
			"code-warden": map[string]any{
				"type":    "remote",
				"url":     mcpURL,
				"enabled": true,
			},
		},
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

// runAgentSDK executes the agent workflow using the goframe/agent SDK.
func (o *Orchestrator) runAgentSDK(ctx context.Context, session *Session, branch string) {
	defer func() {
		if err := o.cleanupWorkspace(session.ID); err != nil {
			o.logger.Error("cleanup failed", "session_id", session.ID, "error", err)
		}
	}()

	// Apply configured timeout to context
	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	defer cancel()

	ws, err := o.prepareAgentWorkspace(ctx, session)
	if err != nil {
		o.logger.Error("runAgentSDK: workspace setup failed", "session_id", session.ID, "error", err)
		session.SetStatus(StatusFailed)
		session.SetError(err.Error())
		return
	}
	defer ws.logFile.Close()
	defer o.mcpServer.UnregisterWorkspace(session.ID)

	o.logger.Info("🛠️ IMPLEMENTATION: Starting OpenCode SDK agent",
		"session_id", session.ID,
		"working_dir", ws.dir,
		"opencode_url", o.config.OpenCodeURL,
		"timeout", o.config.Timeout)

	// Create agent and run implementation
	result, err := o.runSDKFeedbackLoop(ctx, session, ws, branch)

	// Unregister workspace from global registry
	if o.globalMCPRegistry != nil {
		if err := o.globalMCPRegistry.UnregisterWorkspaceBySessionID(session.ID); err != nil {
			o.logger.Warn("failed to unregister workspace from global registry",
				"session_id", session.ID, "error", err)
		}
	}

	// Update session state
	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	if err != nil {
		errMsg := fmt.Sprintf("Agent implementation failed: %v", err)
		session.SetStatus(StatusFailed)
		session.SetError(errMsg)
		o.logger.Error("runAgentSDK: implementation failed",
			"session_id", session.ID,
			"error", err)
		o.postSessionFailed(ctx, session, errMsg)
		return
	}

	session.SetResult(result)
	session.SetStatus(StatusCompleted)
	o.postSessionCompleted(ctx, session, result)

	o.logger.Info("runAgentSDK: agent session completed successfully",
		"session_id", session.ID,
		"pr_number", result.PRNumber,
		"pr_url", result.PRURL,
		"branch", branch,
		"files_changed", len(result.FilesChanged),
		"duration", session.CompletedAt.Sub(session.StartedAt))
}

// runSDKFeedbackLoop creates and runs the feedback loop for SDK mode.
func (o *Orchestrator) runSDKFeedbackLoop(ctx context.Context, session *Session, ws *agentWorkspace, branch string) (*Result, error) {
	// Safely construct MCP URL
	mcpURL, err := url.Parse(fmt.Sprintf("http://%s/sse", o.config.MCPAddr))
	if err != nil {
		return nil, fmt.Errorf("invalid MCP address %q: %w", o.config.MCPAddr, err)
	}
	query := mcpURL.Query()
	query.Set("workspace", session.ID)
	mcpURL.RawQuery = query.Encode()

	mcpRegistry := goframeagent.NewMCPRegistry(
		goframeagent.RemoteMCPServer("code-warden",
			mcpURL.String(),
			goframeagent.WithEnabled(true),
		),
	)

	// Create the agent with path mapping for Docker-based OpenCode
	// Maps host workspace directory to container path
	pathMapping := map[string]string{
		o.config.WorkingDir: "/agent-workspaces",
	}

	ag, err := goframeagent.New(
		goframeagent.WithBaseURL(o.config.OpenCodeURL),
		goframeagent.WithModel(o.config.Model),
		goframeagent.WithMCPRegistry(mcpRegistry),
		goframeagent.WithWorkingDir(ws.dir),
		goframeagent.WithPathMapping(pathMapping),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent: %w", err)
	}

	// Create session
	agSession, err := ag.NewSession(ctx,
		goframeagent.WithTitle(fmt.Sprintf("Issue #%d: %s", session.Issue.Number, truncateString(session.Issue.Title, MaxTitleLength))),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent session: %w", err)
	}
	defer agSession.Close()

	o.logger.Info("runAgentSDK: agent session created",
		"session_id", session.ID,
		"agent_session_id", agSession.ID)

	// Create feedback loop with tracking
	reviewTracker := &reviewTracker{}
	fl := goframeagent.NewFeedbackLoop(ag, agSession,
		goframeagent.WithMaxRetries(o.config.MaxIterations),
		goframeagent.WithReviewHandler(o.createReviewHandler(session, reviewTracker)),
		goframeagent.WithPRHandler(o.createPRHandler(session.Issue, branch)),
	)

	// Build implementation request
	req := goframeagent.ImplementRequest{
		Task:        fmt.Sprintf("Implement GitHub issue #%d: %s", session.Issue.Number, session.Issue.Title),
		Context:     o.buildSDKContext(session.Issue),
		Constraints: o.getSDKConstraints(),
	}

	// Run the feedback loop
	result, err := fl.ImplementWithReview(ctx, req)
	if err != nil {
		return nil, err
	}

	// Parse result
	implResult := &Result{
		Branch:       branch,
		FilesChanged: extractFilesFromImplementation(result.Implementation),
		Verdict:      reviewTracker.GetVerdict(),
		Iterations:   reviewTracker.GetIterations(),
	}

	// Try to extract PR info from response
	if prInfo := extractPRInfo(result.Implementation); prInfo != nil {
		implResult.PRNumber = prInfo.PRNumber
		implResult.PRURL = prInfo.PRURL
	}

	return implResult, nil
}

// createReviewHandler creates a review handler that prompts the agent to use MCP tools.
func (o *Orchestrator) createReviewHandler(agentSession *Session, tracker *reviewTracker) goframeagent.ReviewHandler {
	return func(ctx context.Context, session *goframeagent.Session, implementation string) (*goframeagent.ReviewResult, error) {
		tracker.IncrementIterations()

		o.logger.Info("createReviewHandler: requesting code review",
			"session_id", agentSession.ID,
			"implementation_length", len(implementation),
			"iteration", tracker.GetIterations())

		reviewPrompt := `Please use the review_code tool to review your implementation.

After reviewing, respond with your verdict:
- APPROVE: if the code is ready for PR (all tests pass, no issues)
- REQUEST_CHANGES: if there are issues that need to be fixed (list them clearly)

Remember:
1. Run verification commands first (e.g., make lint, make test)
2. Check that all tests pass
3. Review for code quality and best practices`

		response, err := session.Prompt(ctx, reviewPrompt)
		if err != nil {
			o.logger.Error("createReviewHandler: review request failed", "session_id", agentSession.ID, "error", err)
			return nil, fmt.Errorf("review request failed: %w", err)
		}

		// Get the verdict scoped to this agent session, not global state.
		// This prevents race conditions when multiple sessions run concurrently.
		verdict, _, _ := o.mcpServer.GetReviewBySession(agentSession.ID)
		tracker.SetVerdict(verdict)
		o.postReviewIteration(ctx, agentSession, tracker.GetIterations(), verdict)

		// Determine if approved based on the actual verdict
		approved := verdict == core.VerdictApprove || verdict == core.VerdictComment

		o.logger.Info("createReviewHandler: review completed",
			"session_id", agentSession.ID,
			"verdict", verdict,
			"approved", approved,
			"response_length", len(response.Content))

		return &goframeagent.ReviewResult{
			Approved: approved,
			Feedback: response.Content,
			Score:    DefaultReviewScore,
		}, nil
	}
}

// createPRHandler creates a PR handler that prompts the agent to use MCP tools.
func (o *Orchestrator) createPRHandler(issue Issue, branch string) goframeagent.PRHandler {
	return func(ctx context.Context, session *goframeagent.Session, _ string, review *goframeagent.ReviewResult) error {
		if review == nil || !review.Approved {
			o.logger.Info("createPRHandler: skipping PR creation, review not approved")
			return nil
		}

		o.logger.Info("createPRHandler: requesting PR creation",
			"issue_number", issue.Number,
			"branch", branch)

		prPrompt := fmt.Sprintf(`Use the push_branch tool to push your branch to GitHub, then use the create_pull_request tool to create a pull request with:

Title: "Fix #%d: %s"
Head: %s
Base: main

Include a description of the changes you made.`, issue.Number, issue.Title, branch)

		response, err := session.Prompt(ctx, prPrompt)
		if err != nil {
			o.logger.Error("createPRHandler: PR creation request failed", "error", err)
			return fmt.Errorf("PR creation request failed: %w", err)
		}

		o.logger.Info("createPRHandler: PR creation request completed",
			"response_length", len(response.Content))
		return nil
	}
}

// buildSDKContext creates the context for SDK implementation.
func (o *Orchestrator) buildSDKContext(issue Issue) string {
	var builder strings.Builder

	// Add project context from RAG
	if o.repo != nil && o.repo.GeneratedContext != "" {
		builder.WriteString("## Project Context & Architecture\n")
		builder.WriteString(o.repo.GeneratedContext)
		builder.WriteString("\n\n")
	}

	// Add issue details
	builder.WriteString("## GitHub Issue\n")
	fmt.Fprintf(&builder, "Number: %d\n", issue.Number)
	fmt.Fprintf(&builder, "Title: %s\n", issue.Title)

	if issue.Body != "" {
		fmt.Fprintf(&builder, "\nDescription:\n%s\n", issue.Body)
	}

	if issue.Instructions != "" {
		fmt.Fprintf(&builder, "\nAdditional Instructions:\n%s\n", issue.Instructions)
	}

	// Add custom instructions from repo config
	if o.repoConfig != nil && len(o.repoConfig.CustomInstructions) > 0 {
		builder.WriteString("\n## Custom Instructions\n")
		for _, instruction := range o.repoConfig.CustomInstructions {
			fmt.Fprintf(&builder, "- %s\n", instruction)
		}
	}

	// Add MCP server info
	builder.WriteString("\n## Available Tools\n")
	fmt.Fprintf(&builder, "Connect to MCP server at http://%s to use these tools:\n", o.config.MCPAddr)
	builder.WriteString("- search_code(query, limit, chunk_type): Search the codebase using RAG\n")
	builder.WriteString("- get_arch_context(directory): Get architectural context for a directory\n")
	builder.WriteString("- get_symbol(name): Get definition of a type or function\n")
	builder.WriteString("- get_structure(): Get project structure\n")
	builder.WriteString("- review_code(diff): Request code review\n")
	builder.WriteString("- push_branch(branch): Push branch to GitHub\n")
	builder.WriteString("- create_pull_request(title, body, head, base): Create a GitHub PR\n")

	return builder.String()
}

// getSDKConstraints returns implementation constraints for SDK mode.
func (o *Orchestrator) getSDKConstraints() []string {
	constraints := []string{
		"Start by exploring the codebase using MCP tools (search_code, get_arch_context)",
		"Understand the existing code structure before making changes",
		"Run verification commands before requesting review",
		"All tests must pass before creating PR",
		"Push your branch to GitHub before creating PR",
	}

	if o.repoConfig != nil && len(o.repoConfig.VerifyCommands) > 0 {
		for _, cmd := range o.repoConfig.VerifyCommands {
			constraints = append(constraints, fmt.Sprintf("Run: %s", cmd))
		}
	} else {
		constraints = append(constraints, "Run: make lint", "Run: make test")
	}

	return constraints
}

// extractFilesFromImplementation extracts changed files from the implementation response.
func extractFilesFromImplementation(implementation string) []string {
	// Try to extract files from common patterns
	var files []string
	lines := strings.Split(implementation, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Look for file patterns
		if strings.Contains(line, "modified:") ||
			strings.Contains(line, "created:") ||
			strings.Contains(line, "deleted:") ||
			strings.Contains(line, "File:") {
			parts := strings.Fields(line)
			for _, part := range parts {
				// Check if it looks like a file path
				if strings.Contains(part, "/") || strings.Contains(part, ".") {
					cleanPath := strings.Trim(part, ":")
					if !contains(files, cleanPath) {
						files = append(files, cleanPath)
					}
				}
			}
		}
	}

	if len(files) == 0 {
		files = []string{}
	}
	return files
}

// extractPRInfo extracts PR number and URL from implementation response.
func extractPRInfo(implementation string) *struct {
	PRNumber int
	PRURL    string
} {
	// Look for PR URL patterns
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

// contains checks if a string is in a slice.
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// reviewTracker tracks the review status and iteration count.
type reviewTracker struct {
	mu         sync.Mutex
	verdict    string // The actual verdict from the review tool (e.g., "APPROVE", "REQUEST_CHANGES")
	iterations int
}

func (t *reviewTracker) SetVerdict(verdict string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.verdict = verdict
}

func (t *reviewTracker) GetVerdict() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.verdict == core.VerdictApprove || t.verdict == core.VerdictComment {
		return "APPROVED"
	}
	if t.verdict == "" {
		return "UNKNOWN"
	}
	return t.verdict
}

func (t *reviewTracker) IncrementIterations() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.iterations++
}

func (t *reviewTracker) GetIterations() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.iterations
}
