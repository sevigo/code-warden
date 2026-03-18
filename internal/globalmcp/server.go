// Package globalmcp provides a global MCP server that runs continuously
// alongside the main application server, providing agent tools for CLI
// and external integrations.
package globalmcp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/code-warden/internal/config"
)

const (
	defaultWriteTimeout   = 30 * time.Minute
	defaultIdleTimeout    = 120 * time.Second
	proxyTimeout          = 30 * time.Second
	proxyHandshakeTimeout = 10 * time.Second
)

type Server struct {
	addr        string
	logger      *slog.Logger
	httpServer  *http.Server
	registry    *WorkspaceRegistry
	version     string
	apiKey      string // Optional API key for authentication
	ready       chan struct{}
	readyOnce   sync.Once
	startupOnce sync.Once
	mu          sync.RWMutex
}

func NewServer(cfg *config.Config, logger *slog.Logger, registry *WorkspaceRegistry) *Server {
	return &Server{
		addr:     cfg.Agent.MCPAddr,
		logger:   logger,
		registry: registry,
		version:  "1.0.0",
		ready:    make(chan struct{}),
	}
}

// SetAPIKey sets an optional API key for authenticating sensitive endpoints.
func (s *Server) SetAPIKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiKey = key
}

// authenticate validates the API key for sensitive endpoints.
func (s *Server) authenticate(r *http.Request) bool {
	s.mu.RLock()
	apiKey := s.apiKey
	s.mu.RUnlock()

	// If no API key is configured, allow access (development mode)
	if apiKey == "" {
		return true
	}

	// Check Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false
	}

	// Support both "Bearer <token>" and "<token>" formats
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) == 2 && parts[0] == "Bearer" {
		return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(apiKey)) == 1
	}
	return subtle.ConstantTimeCompare([]byte(authHeader), []byte(apiKey)) == 1
}

func (s *Server) Start(ctx context.Context) error {
	if s.addr == "" {
		s.logger.Info("MCP server address not configured, skipping")
		return nil
	}

	var startErr error
	s.startupOnce.Do(func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", s.handleHealth)
		mux.HandleFunc("/tools", s.handleListTools)
		mux.HandleFunc("/version", s.handleVersion)
		mux.HandleFunc("/status", s.handleStatus)
		mux.HandleFunc("/workspace", s.handleCreateWorkspace)
		mux.HandleFunc("/workspaces", s.handleListWorkspaces)
		mux.HandleFunc("/sse", s.handleSSE)
		mux.HandleFunc("/message", s.handleMessage)

		s.mu.Lock()
		s.httpServer = &http.Server{
			Addr:              s.addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      defaultWriteTimeout,
			IdleTimeout:       defaultIdleTimeout,
		}
		s.mu.Unlock()

		go func() {
			s.logger.Info("starting global MCP HTTP server", "addr", s.addr)
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.logger.Error("MCP HTTP server failed", "error", err, "addr", s.addr)
			}
		}()

		startErr = s.waitForReady(ctx)
	})

	return startErr
}

const (
	maxAttempts        = 50
	retryDelay         = 100 * time.Millisecond
	startupTimeout     = 5 * time.Second
	healthCheckTimeout = 1 * time.Second
)

func (s *Server) waitForReady(ctx context.Context) error {
	client := &http.Client{
		Timeout: healthCheckTimeout,
	}

	for range maxAttempts {
		select {
		case <-ctx.Done():
			return s.shutdownOnFailure()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/health", s.addr), nil)
		if err != nil {
			time.Sleep(retryDelay)
			continue
		}

		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			s.readyOnce.Do(func() {
				close(s.ready)
			})
			s.logger.Info("global MCP HTTP server is ready", "addr", s.addr)
			return nil
		}
		time.Sleep(retryDelay)
	}

	return s.shutdownOnFailure()
}

func (s *Server) shutdownOnFailure() error {
	s.mu.RLock()
	server := s.httpServer
	s.mu.RUnlock()

	if server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), startupTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			s.logger.Error("failed to shutdown MCP server after startup failure", "error", err)
		}
	}

	return fmt.Errorf("timeout waiting for MCP server to start on %s", s.addr)
}

func (s *Server) Stop(ctx context.Context) error {
	s.mu.RLock()
	server := s.httpServer
	s.mu.RUnlock()

	if server == nil {
		return nil
	}

	s.logger.Info("stopping global MCP HTTP server")

	// Close the registry to stop cleanup goroutine
	if s.registry != nil {
		s.registry.Close()
	}

	return server.Shutdown(ctx)
}

// handleHealth provides a health check endpoint.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "ok",
		"service":    "code-warden-mcp",
		"version":    s.version,
		"workspaces": s.countActiveWorkspaces(),
	}); err != nil {
		s.logger.Error("failed to encode health response", "error", err)
	}
}

// handleListTools returns available MCP tools.
func (s *Server) handleListTools(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	tools := []map[string]interface{}{
		{
			"name":               "search_code",
			"description":        "Search code using semantic search",
			"requires_workspace": true,
		},
		{
			"name":               "get_arch_context",
			"description":        "Get architectural context for the codebase",
			"requires_workspace": true,
		},
		{
			"name":               "get_symbol",
			"description":        "Get symbol definition and usage",
			"requires_workspace": true,
		},
		{
			"name":               "find_usages",
			"description":        "Find all usages of a symbol in the codebase",
			"requires_workspace": true,
		},
		{
			"name":               "get_callers",
			"description":        "Find all functions that call the specified function",
			"requires_workspace": true,
		},
		{
			"name":               "get_callees",
			"description":        "Find all functions called by the specified function",
			"requires_workspace": true,
		},
		{
			"name":               "get_structure",
			"description":        "Get file structure analysis",
			"requires_workspace": true,
		},
		{
			"name":               "review_code",
			"description":        "Perform code review on current changes",
			"requires_workspace": true,
		},
		{
			"name":               "push_branch",
			"description":        "Push changes to a git branch",
			"requires_workspace": true,
		},
		{
			"name":               "create_pull_request",
			"description":        "Create a pull request",
			"requires_workspace": true,
		},
		{
			"name":               "list_workspaces",
			"description":        "List active workspaces",
			"requires_workspace": false,
		},
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tools": tools,
	})
}

// handleVersion returns version information.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"version": s.version,
		"service": "code-warden-mcp",
	})
}

// handleStatus returns server status and active workspaces.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	workspaces := s.registry.ListWorkspaces()
	activeCount := 0
	for _, ws := range workspaces {
		if time.Now().Before(ws.ExpiresAt) {
			activeCount++
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":            "running",
		"active_workspaces": activeCount,
		"total_workspaces":  len(workspaces),
	})
}

// CreateWorkspaceRequest is the request body for creating a workspace.
type CreateWorkspaceRequest struct {
	MCPEndpoint string `json:"mcp_endpoint"`
	Repository  string `json:"repository"`
	SessionID   string `json:"session_id"`
	ProjectRoot string `json:"project_root"`
	PRNumber    int    `json:"pr_number,omitempty"`
	IssueNumber int    `json:"issue_number,omitempty"`
	Branch      string `json:"branch,omitempty"`
}

type CreateWorkspaceResponse struct {
	Token       string `json:"token"`
	ExpiresIn   int    `json:"expires_in"`
	MCPEndpoint string `json:"mcp_endpoint"`
}

// handleCreateWorkspace creates a new workspace (called by agent orchestrator).
// This endpoint is for internal use by job-specific MCP servers to register.
func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CreateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.MCPEndpoint == "" {
		http.Error(w, "mcp_endpoint is required", http.StatusBadRequest)
		return
	}
	if req.Repository == "" {
		http.Error(w, "repository is required", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	if req.ProjectRoot == "" {
		http.Error(w, "project_root is required", http.StatusBadRequest)
		return
	}

	// Register workspace
	token, err := s.registry.RegisterWorkspace(
		req.MCPEndpoint,
		req.Repository,
		req.SessionID,
		req.ProjectRoot,
		WorkspaceMeta{
			PRNumber:    req.PRNumber,
			IssueNumber: req.IssueNumber,
			Branch:      req.Branch,
		},
	)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to register workspace: %v", err), http.StatusInternalServerError)
		return
	}

	// Return token
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(CreateWorkspaceResponse{
		Token:       token,
		ExpiresIn:   int(defaultWorkspaceTTL.Seconds()),
		MCPEndpoint: fmt.Sprintf("http://%s/sse?workspace=%s", s.addr, token),
	})
}

// handleListWorkspaces lists active workspaces (requires authentication).
func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	// Require authentication for this sensitive endpoint
	if !s.authenticate(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	workspaces := s.registry.ListWorkspaces()
	summaries := make([]map[string]interface{}, 0, len(workspaces))

	for _, ws := range workspaces {
		if time.Now().Before(ws.ExpiresAt) {
			summaries = append(summaries, map[string]interface{}{
				"token":      ws.Token[:8] + "...", // Truncate for security
				"repository": ws.Repository,
				"branch":     ws.Metadata.Branch,
			})
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"workspaces": summaries,
	})
}

func (s *Server) countActiveWorkspaces() int {
	workspaces := s.registry.ListWorkspaces()
	count := 0
	for _, ws := range workspaces {
		if time.Now().Before(ws.ExpiresAt) {
			count++
		}
	}
	return count
}

// handleSSE handles SSE connections for MCP protocol.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	// Get workspace from query
	token := r.URL.Query().Get("workspace")

	if token == "" {
		s.handleSSEDiscovery(w, r)
		return
	}

	// Lookup workspace
	info, err := s.registry.GetWorkspace(token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Workspace not found: %v", err), http.StatusNotFound)
		return
	}

	// Proxy SSE connection to job-specific MCP server
	s.proxySSE(w, r, info.MCPEndpoint, token)
}

// handleSSEDiscovery provides workspace discovery when no workspace is specified.
func (s *Server) handleSSEDiscovery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	workspaces := s.registry.ListWorkspaces()
	available := make([]map[string]string, 0)

	for _, ws := range workspaces {
		if time.Now().Before(ws.ExpiresAt) {
			available = append(available, map[string]string{
				"token":      ws.Token[:8] + "...",
				"repository": ws.Repository,
			})
		}
	}

	msg := map[string]interface{}{
		"type":       "workspace_discovery",
		"message":    "No workspace specified. Use ?workspace=token parameter.",
		"workspaces": available,
	}

	data, _ := json.Marshal(msg)
	fmt.Fprintf(w, "event: discovery\ndata: %s\n\n", data)

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// proxySSE proxies SSE connection to a job-specific MCP server.
func (s *Server) proxySSE(w http.ResponseWriter, r *http.Request, mcpEndpoint, token string) {
	targetURL, err := url.Parse(mcpEndpoint)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid MCP endpoint: %v", err), http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		s.logger.Error("SSE proxy error", "error", err, "token", token[:8])
		http.Error(w, fmt.Sprintf("Proxy error: %v", err), http.StatusBadGateway)
	}

	// Set timeouts for SSE proxy
	proxy.Transport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: proxyHandshakeTimeout,
		}).DialContext,
		ResponseHeaderTimeout: proxyHandshakeTimeout,
	}

	// Job-specific MCP server expects sessionId parameter
	r.URL.Path = "/sse"
	r.URL.RawQuery = "sessionId=" + token

	s.logger.Debug("proxying SSE connection", "token", token[:8], "target", mcpEndpoint)
	proxy.ServeHTTP(w, r)
}

// handleMessage handles MCP tool calls.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.URL.Query().Get("workspace")

	if token == "" {
		s.handleMessageNoWorkspace(w, r)
		return
	}

	// Lookup workspace
	info, err := s.registry.GetWorkspace(token)
	if err != nil {
		http.Error(w, fmt.Sprintf("Workspace not found: %v", err), http.StatusNotFound)
		return
	}

	// Proxy MCP message to job-specific server
	s.proxyMCPMessage(w, r, info.MCPEndpoint, token)
}

// handleMessageNoWorkspace returns helpful message when no workspace is specified.
func (s *Server) handleMessageNoWorkspace(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": "workspace_required",
		"message": "Repository-specific tools require a workspace token. " +
			"Start an implementation job with /implement to get a workspace, " +
			"or connect to /sse?workspace=token with a valid token.",
		"available_endpoints": []map[string]string{
			{"path": "/health", "description": "Health check"},
			{"path": "/tools", "description": "List available tools"},
			{"path": "/workspaces", "description": "List active workspaces"},
			{"path": "/sse?workspace={token}", "description": "Connect to workspace"},
		},
	})
}

// proxyMCPMessage proxies MCP messages to a job-specific server.
func (s *Server) proxyMCPMessage(w http.ResponseWriter, r *http.Request, mcpEndpoint, token string) {
	targetURL, err := url.Parse(mcpEndpoint)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid MCP endpoint: %v", err), http.StatusInternalServerError)
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read request: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Create proxy request - job-specific server expects sessionId parameter
	//nolint:gosec // G704: targetURL is constructed from trusted config (MCPAddr), token is validated from registry
	targetReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL.String()+"/message?sessionId="+token, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create proxy request: %v", err), http.StatusInternalServerError)
		return
	}
	targetReq.Header.Set("Content-Type", "application/json")

	// Forward request with timeout
	client := &http.Client{
		Timeout: proxyTimeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: proxyHandshakeTimeout,
			}).DialContext,
			ResponseHeaderTimeout: proxyHandshakeTimeout,
		},
	}
	//nolint:gosec // G704: targetReq targets internal MCP server URL from trusted config
	resp, err := client.Do(targetReq)
	if err != nil {
		s.logger.Error("MCP message proxy error", "error", err, "token", token[:8])
		http.Error(w, fmt.Sprintf("Proxy error: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)

	s.logger.Debug("proxied MCP message", "token", token[:8], "status", resp.StatusCode)
}
