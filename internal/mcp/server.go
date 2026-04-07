// Package mcp provides a Model Context Protocol (MCP) server for code-warden.
// It exposes tools for AI agents to interact with the codebase context stored in Qdrant.
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/mcp/tools"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
)

// Server implements an MCP server for code-warden.
type Server struct {
	store       storage.Store
	vectorStore storage.ScopedVectorStore
	ragService  rag.Service
	ghClient    github.Client
	ghToken     string
	repo        *storage.Repository
	repoConfig  *core.RepoConfig
	projectRoot string
	logger      *slog.Logger

	// Tool registry using goframe/agent
	registry   *goframeagent.Registry
	governance *goframeagent.Governance

	// Comparison models for consensus review (optional)
	comparisonModels []string
	reviewsDir       string

	// SSE session management
	sessionsMu sync.RWMutex
	sessions   map[string]*sseSession

	// Per-session workspace overrides (maps workspace token -> project root path)
	workspacesMu sync.RWMutex
	workspaces   map[string]string

	// Review tracking for PR enforcement.
	// reviewsBySession stores results keyed by MCP session ID, preventing race
	// conditions when multiple agent sessions run concurrently.
	// lastReviewResult is the global fallback for backward compatibility.
	reviewMu         sync.RWMutex
	lastReviewResult *reviewResult
	reviewsBySession map[string]*reviewResult
}

// reviewResult tracks the last code review result for enforcement.
type reviewResult struct {
	Verdict   string
	Timestamp time.Time
	DiffHash  string   // Hash of the reviewed diff to detect changes
	Files     []string // Files that were changed in the reviewed diff
}

// sseSession represents an active SSE connection.
type sseSession struct {
	id          string
	messages    chan []byte
	done        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	projectRoot string // per-session workspace root
}

// Tool represents an MCP tool that can be called by an agent.
// This interface matches goframe/agent.Tool for compatibility.
type Tool interface {
	// Name returns the tool name.
	Name() string
	// Description returns a human-readable description.
	Description() string
	// ParametersSchema returns the JSON schema for the tool's input parameters.
	ParametersSchema() map[string]any
	// Execute runs the tool with the given arguments.
	Execute(ctx context.Context, args map[string]any) (any, error)
}

// Config holds configuration for the MCP server.
type Config struct {
	// Port is the HTTP port for the MCP server.
	Port int
	// ProjectRoot is the path to the repository root.
	ProjectRoot string
	// ComparisonModels are models for consensus review (optional).
	ComparisonModels []string
	// ReviewsDir is the directory to save review artifacts (optional).
	ReviewsDir string
}

// NewServer creates a new MCP server.
func NewServer(
	store storage.Store,
	vectorStore storage.ScopedVectorStore,
	ragService rag.Service,
	ghClient github.Client,
	ghToken string,
	repo *storage.Repository,
	repoConfig *core.RepoConfig,
	projectRoot string,
	logger *slog.Logger,
	config Config,
) *Server {
	s := &Server{
		store:            store,
		vectorStore:      vectorStore,
		ragService:       ragService,
		ghClient:         ghClient,
		ghToken:          ghToken,
		repo:             repo,
		repoConfig:       repoConfig,
		projectRoot:      projectRoot,
		logger:           logger,
		registry:         goframeagent.NewRegistry(),
		sessions:         make(map[string]*sseSession),
		workspaces:       make(map[string]string),
		reviewsBySession: make(map[string]*reviewResult),
		comparisonModels: config.ComparisonModels,
		reviewsDir:       config.ReviewsDir,
	}

	// Register default tools
	s.registerTools()

	return s
}

// registerTools registers all available MCP tools.
func (s *Server) registerTools() {
	// Core code search tools
	s.registry.MustRegisterTool(&tools.SearchCode{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	})
	s.registry.MustRegisterTool(&tools.GetArchContext{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	})
	s.registry.MustRegisterTool(&tools.GetSymbol{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	})
	s.registry.MustRegisterTool(&tools.GetStructure{
		VectorStore: s.vectorStore,
		ProjectRoot: s.projectRoot,
		Logger:      s.logger,
	})
	s.registry.MustRegisterTool(&tools.FindUsages{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	})
	s.registry.MustRegisterTool(&tools.GetCallers{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	})
	s.registry.MustRegisterTool(&tools.GetCallees{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	})
	s.registry.MustRegisterTool(&tools.ReviewCode{
		RagService:       s.ragService,
		Repo:             s.repo,
		RepoConfig:       s.repoConfig,
		ComparisonModels: s.comparisonModels,
		ReviewsDir:       s.reviewsDir,
		ReviewTracker:    s,
		Logger:           s.logger,
	})
	s.registry.MustRegisterTool(&tools.RunCommand{
		RepoConfig:  s.repoConfig,
		ProjectRoot: s.projectRoot,
		Logger:      s.logger,
	})

	// Register GitHub tools if ghClient is available
	if s.ghClient != nil && s.repo != nil {
		owner, name := parseRepoFullName(s.repo.FullName)
		if owner != "" && name != "" {
			s.registry.MustRegisterTool(&tools.CreatePullRequest{
				GHClient:      s.ghClient,
				Repo:          tools.RepoIdentifier{Owner: owner, Name: name},
				ReviewTracker: s,
				Logger:        s.logger,
			})
			s.registry.MustRegisterTool(&tools.ListIssues{
				GHClient: s.ghClient,
				Repo:     tools.RepoIdentifier{Owner: owner, Name: name},
				Logger:   s.logger,
			})
			s.registry.MustRegisterTool(&tools.GetIssue{
				GHClient: s.ghClient,
				Repo:     tools.RepoIdentifier{Owner: owner, Name: name},
				Logger:   s.logger,
			})
			s.registry.MustRegisterTool(&tools.PushBranch{
				ProjectRoot:   s.projectRoot,
				GHToken:       s.ghToken,
				Logger:        s.logger,
				ReviewTracker: s,
			})
		}
	}
}

// parseRepoFullName parses a "owner/name" string into owner and name.
func parseRepoFullName(fullName string) (owner, name string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", fullName
}

// RegisterWorkspace associates a workspace token with a project root path.
// The token is passed by the agent via the SSE connection URL.
func (s *Server) RegisterWorkspace(token, projectRoot string) {
	s.workspacesMu.Lock()
	s.workspaces[token] = projectRoot
	s.workspacesMu.Unlock()
	s.logger.Info("workspace registered", "token", token, "project_root", projectRoot)
}

// UnregisterWorkspace removes a workspace association.
func (s *Server) UnregisterWorkspace(token string) {
	s.workspacesMu.Lock()
	delete(s.workspaces, token)
	s.workspacesMu.Unlock()
	s.logger.Debug("workspace unregistered", "token", token)
}

// SetupGovernance configures the governance layer with security checks.
// If config.EnableGovernance is false, no governance is applied.
// Note: This method is not idempotent - calling it twice replaces the previous governance.
func (s *Server) SetupGovernance(config GovernanceConfig) {
	if !config.EnableGovernance {
		s.logger.Debug("governance disabled, all tools are permitted")
		return
	}

	checks := []goframeagent.IntegrityCheck{}

	// Permission check (allow/deny lists)
	if len(config.AllowedTools) > 0 || len(config.DeniedTools) > 0 {
		permCheck := goframeagent.NewPermissionCheck()
		for _, tool := range config.AllowedTools {
			permCheck.Allow(tool)
		}
		for _, tool := range config.DeniedTools {
			permCheck.Deny(tool)
		}
		checks = append(checks, permCheck)
		s.logger.Info("governance permission checks configured",
			"allowed", len(config.AllowedTools),
			"denied", len(config.DeniedTools))
	}

	// Rate limiting
	if len(config.RateLimits) > 0 {
		rateCheck := goframeagent.NewRateLimitCheck()
		for tool, limit := range config.RateLimits {
			rateCheck.SetLimit(tool, limit)
		}
		checks = append(checks, rateCheck)
		s.logger.Info("governance rate limits configured", "tools", len(config.RateLimits))
	}

	if len(checks) == 0 {
		s.logger.Warn("governance enabled but no rules configured - all tools will pass")
	}

	s.governance = goframeagent.NewGovernance(checks...)
	s.logger.Info("governance layer enabled", "checks", len(checks))
}

// Tools returns all registered tool objects (used by the native in-process agent).
func (s *Server) Tools() []Tool {
	raw := s.registry.List()
	result := make([]Tool, 0, len(raw))
	for _, t := range raw {
		if mct, ok := t.(Tool); ok {
			result = append(result, mct)
		}
	}
	return result
}

// ListTools returns all available tools in deterministic order.
func (s *Server) ListTools() []ToolInfo {
	defs := s.registry.Definitions()
	tools := make([]ToolInfo, 0, len(defs))
	for _, def := range defs {
		fn, ok := def["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		tools = append(tools, ToolInfo{
			Name:        name,
			Description: desc,
			InputSchema: params,
		})
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
	return tools
}

// Review tracking constants
const (
	// maxReviewAge is the maximum age of a review before it's considered stale.
	// This is a policy decision: 30 minutes provides reasonable security while
	// allowing time for agent iteration. Tests should mock time or use short durations.
	maxReviewAge = 30 * time.Minute
)

// RecordReviewBySession stores the review result scoped to the session in ctx.
// Falls back to global state when no session ID is present in ctx.
func (s *Server) RecordReviewBySession(ctx context.Context, verdict, diffHash string) {
	sessionID := tools.SessionIDFromContext(ctx)

	hashLen := 8
	if len(diffHash) < hashLen {
		hashLen = len(diffHash)
	}

	result := &reviewResult{
		Verdict:   verdict,
		Timestamp: time.Now(),
		DiffHash:  diffHash,
	}

	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()

	// Always update global fallback.
	s.lastReviewResult = result

	// Also store per-session when a session ID is available.
	if sessionID != "" {
		s.reviewsBySession[sessionID] = result
		s.logger.Info("review recorded for session",
			"session_id", sessionID,
			"verdict", verdict,
			"diff_hash", diffHash[:hashLen])
	} else {
		s.logger.Info("review recorded (no session context, global only)",
			"verdict", verdict,
			"diff_hash", diffHash[:hashLen])
	}
}

// GetLastReview returns the last review result from global state.
// Used by the orchestrator when it needs the verdict for a specific session via GetReviewBySession.
func (s *Server) GetLastReview() (verdict string, timestamp time.Time, diffHash string) {
	s.reviewMu.RLock()
	defer s.reviewMu.RUnlock()
	if s.lastReviewResult == nil {
		return "", time.Time{}, ""
	}
	return s.lastReviewResult.Verdict, s.lastReviewResult.Timestamp, s.lastReviewResult.DiffHash
}

// GetReviewBySession returns the review result for a specific agent session ID.
// Falls back to global state when no session-specific result exists.
func (s *Server) GetReviewBySession(sessionID string) (verdict string, timestamp time.Time, diffHash string) {
	s.reviewMu.RLock()
	defer s.reviewMu.RUnlock()
	if r, ok := s.reviewsBySession[sessionID]; ok {
		return r.Verdict, r.Timestamp, r.DiffHash
	}
	// Fallback to global for sessions that predated per-session tracking.
	if s.lastReviewResult != nil {
		return s.lastReviewResult.Verdict, s.lastReviewResult.Timestamp, s.lastReviewResult.DiffHash
	}
	return "", time.Time{}, ""
}

// CheckApprovalBySession verifies there is a recent approved review for the session in ctx.
// Falls back to global state when no session ID is present.
func (s *Server) CheckApprovalBySession(ctx context.Context, diffHash string) error {
	sessionID := tools.SessionIDFromContext(ctx)

	s.reviewMu.RLock()
	defer s.reviewMu.RUnlock()

	result := s.lastReviewResult // global fallback
	if sessionID != "" {
		if r, ok := s.reviewsBySession[sessionID]; ok {
			result = r
		}
	}

	if result == nil {
		return fmt.Errorf("no code review found: you must call review_code and receive APPROVE verdict before creating a PR")
	}
	if diffHash != "" && result.DiffHash != "" && diffHash != result.DiffHash {
		return fmt.Errorf("code has changed since last review: please run review_code again")
	}
	if result.Verdict != core.VerdictApprove && result.Verdict != core.VerdictComment {
		return fmt.Errorf("last review verdict was %s (needs APPROVE or COMMENT): fix issues and run review_code again", result.Verdict)
	}
	if time.Since(result.Timestamp) > maxReviewAge {
		return fmt.Errorf("review is stale (older than %v): please run review_code again", maxReviewAge)
	}

	s.logger.Info("PR creation approved",
		"session_id", sessionID,
		"review_verdict", result.Verdict,
		"review_age", time.Since(result.Timestamp))
	return nil
}

// RecordReviewFiles stores the list of files that were changed in the reviewed diff.
// A copy of the slice is stored to prevent aliasing issues.
func (s *Server) RecordReviewFiles(files []string) {
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	if s.lastReviewResult == nil {
		s.lastReviewResult = &reviewResult{}
	}
	// Copy the slice to prevent aliasing issues
	s.lastReviewResult.Files = append([]string(nil), files...)
	s.logger.Info("review files recorded", "file_count", len(files))
}

// GetLastReviewFiles returns a copy of the files from the last review.
// Returns nil if no review has been recorded.
func (s *Server) GetLastReviewFiles() []string {
	s.reviewMu.RLock()
	defer s.reviewMu.RUnlock()
	if s.lastReviewResult == nil {
		return nil
	}
	// Return a copy to prevent aliasing issues
	return append([]string(nil), s.lastReviewResult.Files...)
}

// CallTool executes a tool by name.
// If governance is enabled, it validates the tool call before execution.
func (s *Server) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	// Validate with governance if enabled
	if s.governance != nil {
		if err := s.governance.Validate(ctx, name, args); err != nil {
			s.logger.Warn("tool execution blocked by governance",
				"tool", name,
				"error", err)
			return nil, fmt.Errorf("governance denied: %w", err)
		}
	}

	return s.registry.Execute(ctx, name, args)
}

// ToolInfo represents tool metadata for the MCP protocol.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Request represents a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Error represents a JSON-RPC 2.0 error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP implements http.Handler for the MCP server.
// It supports both SSE transport (GET /sse, POST /message) and direct JSON-RPC (POST /).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/sse":
		s.handleSSE(w, r)
	case "/message":
		s.handleMessage(w, r)
	default:
		// Direct JSON-RPC for backwards compatibility
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleJSONRPC(w, r)
	}
}

// handleSSE handles SSE transport connections.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if flusher is supported
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Generate session ID
	sessionID := generateSessionID()

	// Resolve per-session project root from workspace token
	workspaceToken := r.URL.Query().Get("workspace")
	projectRoot := s.projectRoot // default
	if workspaceToken != "" {
		s.workspacesMu.RLock()
		if override, ok := s.workspaces[workspaceToken]; ok {
			projectRoot = override
		}
		s.workspacesMu.RUnlock()
		s.logger.Debug("SSE session workspace resolved", "session_id", sessionID, "workspace", workspaceToken, "project_root", projectRoot)
	}

	// Create a per-session context that is cancelled when the client disconnects
	//nolint:gosec // G118: sessionCancel stored in session.cancel and called in defer below
	sessionCtx, sessionCancel := context.WithCancel(r.Context())
	session := &sseSession{
		id:          sessionID,
		messages:    make(chan []byte, 100),
		done:        make(chan struct{}),
		ctx:         sessionCtx,
		cancel:      sessionCancel,
		projectRoot: projectRoot,
	}

	s.sessionsMu.Lock()
	s.sessions[sessionID] = session
	s.sessionsMu.Unlock()

	defer func() {
		session.cancel() // Cancel context first
		s.sessionsMu.Lock()
		delete(s.sessions, sessionID)
		s.sessionsMu.Unlock()
		close(session.done)
	}()

	// Send endpoint event - tells client where to send messages
	endpointEvent := fmt.Sprintf("event: endpoint\ndata: /message?sessionId=%s\n\n", sessionID)
	fmt.Fprint(w, endpointEvent)
	flusher.Flush()

	s.logger.Info("SSE client connected", "session_id", sessionID)

	// Keep connection alive and send messages
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			s.logger.Info("SSE client disconnected", "session_id", sessionID)
			return
		case msg := <-session.messages:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			// Send keepalive comment
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// handleMessage handles JSON-RPC messages from SSE clients.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get session ID from query
	sessionID := r.URL.Query().Get("sessionId")

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error")
		return
	}

	// Process the request with a context that combines the POST request context
	// and the long-lived SSE session context.
	s.sessionsMu.RLock()
	session, exists := s.sessions[sessionID]
	s.sessionsMu.RUnlock()

	var toolCtx context.Context
	var cancel context.CancelFunc

	if exists {
		// Create a context that is cancelled if either the POST request ends
		// OR the underlying SSE stream is closed.
		toolCtx, cancel = context.WithCancel(r.Context())
		defer cancel()
		go func() {
			select {
			case <-session.ctx.Done():
				cancel()
			case <-toolCtx.Done():
			}
		}()
		// Inject per-session project root and session ID into tool context.
		// The session ID is used by review_code and create_pull_request to scope
		// review results per session, preventing race conditions under concurrent sessions.
		if session.projectRoot != "" {
			toolCtx = tools.WithProjectRoot(toolCtx, session.projectRoot)
		}
		toolCtx = tools.WithSessionID(toolCtx, sessionID)
	} else {
		toolCtx = r.Context()
	}

	result, err := s.processRequest(toolCtx, &req)
	if err != nil {
		s.writeError(w, req.ID, err.Code, err.Message)
		return
	}

	// If we have a session, send response via SSE
	if sessionID != "" {
		s.sessionsMu.RLock()
		session, ok := s.sessions[sessionID]
		s.sessionsMu.RUnlock()

		if ok {
			resp := Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  result,
			}
			respBytes, _ := json.Marshal(resp)
			select {
			case session.messages <- respBytes:
				// Message sent successfully
			case <-session.done:
				// Session was closed, client disconnected
				s.logger.Debug("session closed before message sent", "session_id", sessionID)
			case <-time.After(5 * time.Second):
				// Timeout to avoid blocking forever
				s.logger.Warn("timeout sending message to session", "session_id", sessionID)
			}
		}
	}

	// Also send HTTP response for acknowledgement
	s.writeResult(w, req.ID, result)
}

// handleJSONRPC handles direct JSON-RPC requests (backwards compatibility).
func (s *Server) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error")
		return
	}

	s.logger.Info("MCP JSON-RPC request received",
		"method", req.Method,
		"id", req.ID)

	result, err := s.processRequest(r.Context(), &req)
	if err != nil {
		s.writeError(w, req.ID, err.Code, err.Message)
		return
	}

	s.writeResult(w, req.ID, result)
}

// jsonRPCError represents a JSON-RPC error.
type jsonRPCError struct {
	Code    int
	Message string
}

// processRequest processes a JSON-RPC request and returns the result.
func (s *Server) processRequest(ctx context.Context, req *Request) (any, *jsonRPCError) {
	switch req.Method {
	case "notifications/initialized",
		"notifications/cancelled",
		"notifications/progress",
		"notifications/roots/list_changed",
		"notifications/tools/list_changed":
		return map[string]any{}, nil

	case "tools/list":
		tools := s.ListTools()
		return map[string]any{
			"tools": tools,
		}, nil

	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params"}
		}
		icon := getToolIcon(params.Name)
		s.logger.Info(fmt.Sprintf("%s MCP tool call started", icon), "tool", params.Name)
		result, err := s.CallTool(ctx, params.Name, params.Arguments)
		if err != nil {
			s.logger.Error(fmt.Sprintf("%s MCP tool call failed", icon), "tool", params.Name, "error", err)
			return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
		}
		s.logger.Info(fmt.Sprintf("%s MCP tool call completed", icon), "tool", params.Name, "result_type", fmt.Sprintf("%T", result))
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": mustMarshal(result)},
			},
		}, nil

	case "initialize":
		s.logger.Info("MCP client initializing")
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]any{
				"name":    "code-warden-mcp",
				"version": "1.0.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
		}, nil

	case "ping":
		s.logger.Debug("MCP ping received")
		return map[string]any{}, nil

	default:
		s.logger.Warn("MCP unknown method", "method", req.Method)
		return nil, &jsonRPCError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) writeResult(w http.ResponseWriter, id any, result any) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("failed to encode response", "error", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, id any, code int, message string) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	// JSON-RPC over HTTP should return 200 OK even for errors
	// The error is indicated in the JSON body, not the HTTP status
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("failed to encode error response", "error", err)
	}
}

func mustMarshal(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func getToolIcon(name string) string {
	switch name {
	case "search_code":
		return "🔍"
	case "get_arch_context":
		return "🏛️"
	case "get_symbol":
		return "🧩"
	case "get_structure":
		return "📂"
	case "view_file":
		return "📖"
	case "list_dir":
		return "📁"
	case "grep_search":
		return "🔎"
	case "review_code":
		return "⚖️"
	case "push_branch":
		return "🚀"
	case "create_pull_request":
		return "📦"
	case "list_issues", "get_issue":
		return "🎫"
	case "run_command":
		return "💻"
	case "write_file":
		return "💾"
	case "replace_file_content", "multi_replace_file_content":
		return "📝"
	default:
		return "🛠️"
	}
}

// generateSessionID creates a unique session ID for SSE connections.
func generateSessionID() string {
	b := make([]byte, 16)
	// crypto/rand.Read always returns len(b), nil on supported platforms
	_, _ = rand.Read(b)
	return fmt.Sprintf("sess_%x", b)
}
