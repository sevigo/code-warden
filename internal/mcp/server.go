// Package mcp provides a Model Context Protocol (MCP) server for code-warden.
// It exposes tools for AI agents to interact with the codebase context stored in Qdrant.
package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
)

// Server implements an MCP server for code-warden.
type Server struct {
	store       storage.Store
	vectorStore storage.ScopedVectorStore
	ragService  rag.Service
	ghClient    github.Client
	repo        *storage.Repository
	repoConfig  *core.RepoConfig
	projectRoot string
	logger      *slog.Logger
	tools       map[string]Tool

	// SSE session management
	sessionsMu sync.RWMutex
	sessions   map[string]*sseSession
}

// sseSession represents an active SSE connection.
type sseSession struct {
	id       string
	messages chan []byte
	done     chan struct{}
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
}

// NewServer creates a new MCP server.
func NewServer(
	store storage.Store,
	vectorStore storage.ScopedVectorStore,
	ragService rag.Service,
	ghClient github.Client,
	repo *storage.Repository,
	repoConfig *core.RepoConfig,
	projectRoot string,
	logger *slog.Logger,
) *Server {
	s := &Server{
		store:       store,
		vectorStore: vectorStore,
		ragService:  ragService,
		ghClient:    ghClient,
		repo:        repo,
		repoConfig:  repoConfig,
		projectRoot: projectRoot,
		logger:      logger,
		tools:       make(map[string]Tool),
		sessions:    make(map[string]*sseSession),
	}

	// Register default tools
	s.registerTools()

	return s
}

// registerTools registers all available MCP tools.
func (s *Server) registerTools() {
	s.tools["search_code"] = &SearchCodeTool{
		vectorStore: s.vectorStore,
		logger:      s.logger,
	}
	s.tools["get_arch_context"] = &GetArchContextTool{
		vectorStore: s.vectorStore,
		logger:      s.logger,
	}
	s.tools["get_symbol"] = &GetSymbolTool{
		vectorStore: s.vectorStore,
		logger:      s.logger,
	}
	s.tools["get_structure"] = &GetStructureTool{
		vectorStore: s.vectorStore,
		projectRoot: s.projectRoot,
		logger:      s.logger,
	}
	s.tools["review_code"] = &ReviewCodeTool{
		ragService: s.ragService,
		repo:       s.repo,
		repoConfig: s.repoConfig,
		logger:     s.logger,
	}
}

// ListTools returns all available tools.
func (s *Server) ListTools() []ToolInfo {
	tools := make([]ToolInfo, 0, len(s.tools))
	for name, tool := range s.tools {
		tools = append(tools, ToolInfo{
			Name:        name,
			Description: tool.Description(),
			InputSchema: tool.InputSchema(),
		})
	}
	return tools
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

	// Create session
	session := &sseSession{
		id:       sessionID,
		messages: make(chan []byte, 100),
		done:     make(chan struct{}),
	}

	s.sessionsMu.Lock()
	s.sessions[sessionID] = session
	s.sessionsMu.Unlock()

	defer func() {
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
	ticker := time.NewTicker(30 * time.Second)
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

	// Process the request
	result, err := s.processRequest(r.Context(), &req)
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
	// Log all incoming requests for debugging
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))

	s.logger.Info("MCP JSON-RPC request received",
		"method", r.Method,
		"path", r.URL.Path,
		"body", string(body))

	var req Request
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error")
		return
	}

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
	case "tools/list":
		s.logger.Info("MCP tool call: tools/list")
		tools := s.ListTools()
		s.logger.Info("MCP tool response: tools/list", "tool_count", len(tools))
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
		s.logger.Info("MCP tool call started", "tool", params.Name, "arguments", params.Arguments)
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
