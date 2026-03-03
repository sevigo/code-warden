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
	tools       map[string]Tool

	// Comparison models for consensus review (optional)
	comparisonModels []string
	reviewsDir       string

	// SSE session management
	sessionsMu sync.RWMutex
	sessions   map[string]*sseSession

	// Per-session workspace overrides (maps workspace token -> project root path)
	workspacesMu sync.RWMutex
	workspaces   map[string]string

	// Review tracking for PR enforcement
	reviewMu         sync.RWMutex
	lastReviewResult *reviewResult
}

// reviewResult tracks the last code review result for enforcement.
type reviewResult struct {
	Verdict   string
	Timestamp time.Time
	DiffHash  string // Hash of the reviewed diff to detect changes
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
type Tool interface {
	// Name returns the tool name.
	Name() string
	// Description returns a human-readable description.
	Description() string
	// InputSchema returns the JSON schema for the tool's input parameters.
	InputSchema() map[string]any
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
		tools:            make(map[string]Tool),
		sessions:         make(map[string]*sseSession),
		workspaces:       make(map[string]string),
		comparisonModels: config.ComparisonModels,
		reviewsDir:       config.ReviewsDir,
	}

	// Register default tools
	s.registerTools()

	return s
}

// registerTools registers all available MCP tools.
func (s *Server) registerTools() {
	s.tools["search_code"] = &tools.SearchCode{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	}
	s.tools["get_arch_context"] = &tools.GetArchContext{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	}
	s.tools["get_symbol"] = &tools.GetSymbol{
		VectorStore: s.vectorStore,
		Logger:      s.logger,
	}
	s.tools["get_structure"] = &tools.GetStructure{
		VectorStore: s.vectorStore,
		ProjectRoot: s.projectRoot,
		Logger:      s.logger,
	}
	s.tools["review_code"] = &tools.ReviewCode{
		RagService:       s.ragService,
		Repo:             s.repo,
		RepoConfig:       s.repoConfig,
		ComparisonModels: s.comparisonModels,
		ReviewsDir:       s.reviewsDir,
		ReviewTracker:    s,
		Logger:           s.logger,
	}

	// Register GitHub tools if ghClient is available
	if s.ghClient != nil && s.repo != nil {
		// Parse owner/name from FullName
		owner, name := parseRepoFullName(s.repo.FullName)
		if owner != "" && name != "" {
			s.tools["create_pull_request"] = &tools.CreatePullRequest{
				GHClient:      s.ghClient,
				Repo:          tools.RepoIdentifier{Owner: owner, Name: name},
				ReviewTracker: s, // Enforces approved review before PR
				Logger:        s.logger,
			}
			s.tools["list_issues"] = &tools.ListIssues{
				GHClient: s.ghClient,
				Repo:     tools.RepoIdentifier{Owner: owner, Name: name},
				Logger:   s.logger,
			}
			s.tools["get_issue"] = &tools.GetIssue{
				GHClient: s.ghClient,
				Repo:     tools.RepoIdentifier{Owner: owner, Name: name},
				Logger:   s.logger,
			}
			s.tools["push_branch"] = &tools.PushBranch{
				ProjectRoot: s.projectRoot,
				GHToken:     s.ghToken,
				Logger:      s.logger,
			}
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

// ListTools returns all available tools in deterministic order.
func (s *Server) ListTools() []ToolInfo {
	tools := make([]ToolInfo, 0, len(s.tools))
	for name, tool := range s.tools {
		tools = append(tools, ToolInfo{
			Name:        name,
			Description: tool.Description(),
			InputSchema: tool.InputSchema(),
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

// RecordReview stores the review result for enforcement.
func (s *Server) RecordReview(verdict, diffHash string) {
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	s.lastReviewResult = &reviewResult{
		Verdict:   verdict,
		Timestamp: time.Now(),
		DiffHash:  diffHash,
	}
	// Truncate diff hash for logging
	hashLen := 8
	if len(diffHash) < hashLen {
		hashLen = len(diffHash)
	}
	s.logger.Info("review recorded for PR enforcement",
		"verdict", verdict,
		"diff_hash", diffHash[:hashLen])
}

// GetLastReview returns the last review result.
func (s *Server) GetLastReview() (verdict string, timestamp time.Time, diffHash string) {
	s.reviewMu.RLock()
	defer s.reviewMu.RUnlock()
	if s.lastReviewResult == nil {
		return "", time.Time{}, ""
	}
	return s.lastReviewResult.Verdict, s.lastReviewResult.Timestamp, s.lastReviewResult.DiffHash
}

// CheckApproval verifies if there's a recent approved review.
func (s *Server) CheckApproval(diffHash string) error {
	s.reviewMu.RLock()
	defer s.reviewMu.RUnlock()

	if s.lastReviewResult == nil {
		return fmt.Errorf("no code review found: you must call review_code and receive APPROVE verdict before creating a PR")
	}

	// Check if review is for the same code (diff hash matches)
	if diffHash != "" && s.lastReviewResult.DiffHash != "" && diffHash != s.lastReviewResult.DiffHash {
		return fmt.Errorf("code has changed since last review: please run review_code again")
	}

	// Check if review is approved or a comment
	if s.lastReviewResult.Verdict != core.VerdictApprove && s.lastReviewResult.Verdict != core.VerdictComment {
		return fmt.Errorf("last review verdict was %s (needs APPROVE or COMMENT): fix issues and run review_code again", s.lastReviewResult.Verdict)
	}

	// Check if review is recent
	if time.Since(s.lastReviewResult.Timestamp) > maxReviewAge {
		return fmt.Errorf("review is stale (older than %v): please run review_code again", maxReviewAge)
	}

	s.logger.Info("PR creation approved",
		"review_verdict", s.lastReviewResult.Verdict,
		"review_age", time.Since(s.lastReviewResult.Timestamp))
	return nil
}

// CallTool executes a tool by name.
func (s *Server) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	tool, ok := s.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return tool.Execute(ctx, args)
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
		// Inject per-session project root into tool context
		if session.projectRoot != "" {
			toolCtx = tools.WithProjectRoot(toolCtx, session.projectRoot)
		}
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
		s.logger.Info("MCP tool call started", "tool", params.Name)
		result, err := s.CallTool(ctx, params.Name, params.Arguments)
		if err != nil {
			s.logger.Error("MCP tool call failed", "tool", params.Name, "error", err)
			return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
		}
		s.logger.Info("MCP tool call completed", "tool", params.Name, "result_type", fmt.Sprintf("%T", result))
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

// generateSessionID creates a unique session ID for SSE connections.
func generateSessionID() string {
	b := make([]byte, 16)
	// crypto/rand.Read always returns len(b), nil on supported platforms
	_, _ = rand.Read(b)
	return fmt.Sprintf("sess_%x", b)
}
