// Package mcp provides a Model Context Protocol (MCP) server for code-warden.
// It exposes tools for AI agents to interact with the codebase context stored in Qdrant.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
)

// Server implements an MCP server for code-warden.
type Server struct {
	store        storage.Store
	vectorStore  storage.ScopedVectorStore
	ragService   rag.Service
	ghClient     github.Client
	repo         *storage.Repository
	repoConfig   *core.RepoConfig
	projectRoot  string
	logger       *slog.Logger
	tools        map[string]Tool
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

// MCPRequest represents a JSON-RPC 2.0 request.
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCPResponse represents a JSON-RPC 2.0 response.
type MCPResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *MCPError `json:"error,omitempty"`
}

// MCPError represents a JSON-RPC 2.0 error.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP implements http.Handler for the MCP server.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req MCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error")
		return
	}

	var result any
	var err error

	switch req.Method {
	case "tools/list":
		result = map[string]any{
			"tools": s.ListTools(),
		}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.writeError(w, req.ID, -32602, "invalid params")
			return
		}
		result, err = s.CallTool(r.Context(), params.Name, params.Arguments)
		if err != nil {
			s.writeError(w, req.ID, -32603, err.Error())
			return
		}
		result = map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": mustMarshal(result)},
			},
		}
	case "initialize":
		result = map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]any{
				"name":    "code-warden-mcp",
				"version": "1.0.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
		}
	default:
		s.writeError(w, req.ID, -32601, "method not found")
		return
	}

	s.writeResult(w, req.ID, result)
}

func (s *Server) writeResult(w http.ResponseWriter, id any, result any) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) writeError(w http.ResponseWriter, id any, code int, message string) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &MCPError{
			Code:    code,
			Message: message,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(resp)
}

func mustMarshal(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}