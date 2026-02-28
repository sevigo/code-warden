// Package main provides a standalone MCP test server for validating tool calling.
//
// This is a simple MCP server that implements three fun tools:
//   - roll_dice: Roll dice and get random results (great for making decisions!)
//   - echo: Echo back messages with customizable enthusiasm levels
//   - get_time: Get the current time in various formats
//
// Usage:
//
//	go run ./cmd/mcp-test
//	curl http://127.0.0.1:8082/tools
//	curl -X POST http://127.0.0.1:8082/ -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"roll_dice","arguments":{"sides":20,"count":3}}}'
//
// This server is useful for:
//   - Testing MCP client implementations
//   - Validating tool discovery and execution
//   - Debugging SSE transport issues
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// Tool represents an MCP tool.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Execute(ctx context.Context, args map[string]any) (any, error)
}

// Server implements a simple MCP server.
type Server struct {
	tools      map[string]Tool
	toolsMu    sync.RWMutex
	sessions   map[string]*sseSession
	sessionsMu sync.RWMutex
}

type sseSession struct {
	id       string
	messages chan []byte
	done     chan struct{}
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

// DiceRollTool rolls dice and returns the result.
type DiceRollTool struct{}

func (t *DiceRollTool) Name() string { return "roll_dice" }
func (t *DiceRollTool) Description() string {
	return "Roll dice and return the result. Great for making random decisions!"
}
func (t *DiceRollTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sides": map[string]any{
				"type":        "integer",
				"description": "Number of sides on the die (default 6)",
				"default":     6,
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of dice to roll (default 1)",
				"default":     1,
			},
		},
	}
}
func (t *DiceRollTool) Execute(_ context.Context, args map[string]any) (any, error) {
	sides := 6
	if s, ok := args["sides"].(float64); ok {
		sides = int(s)
	}
	if sides < 2 {
		return nil, fmt.Errorf("sides must be at least 2")
	}

	count := 1
	if c, ok := args["count"].(float64); ok {
		count = int(c)
	}
	if count < 1 {
		return nil, fmt.Errorf("count must be at least 1")
	}
	if count > 100 {
		return nil, fmt.Errorf("count cannot exceed 100")
	}

	rolls := make([]int, count)
	total := 0
	for i := range count {
		roll := rand.IntN(sides) + 1 //nolint:gosec // G404: weak random is fine for dice game
		rolls[i] = roll
		total += roll
	}

	return map[string]any{
		"rolls":   rolls,
		"total":   total,
		"sides":   sides,
		"count":   count,
		"message": fmt.Sprintf("Rolled %d dice with %d sides: %v (total: %d)", count, sides, rolls, total),
	}, nil
}

// EchoTool echoes back the message with a fun twist.
type EchoTool struct{}

func (t *EchoTool) Name() string        { return "echo" }
func (t *EchoTool) Description() string { return "Echo back your message with a friendly greeting" }
func (t *EchoTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "The message to echo back",
			},
			"enthusiasm": map[string]any{
				"type":        "string",
				"description": "Level of enthusiasm: 'low', 'medium', or 'high'",
				"default":     "medium",
				"enum":        []string{"low", "medium", "high"},
			},
		},
		"required": []string{"message"},
	}
}
func (t *EchoTool) Execute(_ context.Context, args map[string]any) (any, error) {
	message, ok := args["message"].(string)
	if !ok {
		return nil, fmt.Errorf("message is required")
	}

	enthusiasm := "medium"
	if e, ok := args["enthusiasm"].(string); ok {
		enthusiasm = e
	}

	var prefix, suffix string
	switch enthusiasm {
	case "high":
		prefix = "🎉 WOW! You said: "
		suffix = "!!! THAT'S AMAZING!!! 🎊"
	case "low":
		prefix = "You said: "
		suffix = "."
	default:
		prefix = "👋 You said: "
		suffix = "!"
	}

	return map[string]any{
		"original":   message,
		"enthusiasm": enthusiasm,
		"response":   prefix + message + suffix,
	}, nil
}

// TimeTool returns the current time in various formats.
type TimeTool struct{}

func (t *TimeTool) Name() string        { return "get_time" }
func (t *TimeTool) Description() string { return "Get the current time in various formats" }
func (t *TimeTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "Time zone (e.g., 'UTC', 'America/New_York')",
				"default":     "Local",
			},
		},
	}
}
func (t *TimeTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	now := time.Now()

	return map[string]any{
		"iso":         now.Format(time.RFC3339),
		"unix":        now.Unix(),
		"readable":    now.Format("3:04 PM on Monday, January 2, 2006"),
		"hour":        now.Hour(),
		"minute":      now.Minute(),
		"day_of_week": now.Format("Monday"),
	}, nil
}

// NewServer creates a new MCP test server.
func NewServer() *Server {
	s := &Server{
		tools:    make(map[string]Tool),
		sessions: make(map[string]*sseSession),
	}
	s.tools["roll_dice"] = &DiceRollTool{}
	s.tools["echo"] = &EchoTool{}
	s.tools["get_time"] = &TimeTool{}
	return s
}

// ListTools returns all available tools.
func (s *Server) ListTools() []map[string]any {
	s.toolsMu.RLock()
	defer s.toolsMu.RUnlock()

	tools := make([]map[string]any, 0, len(s.tools))
	for name, tool := range s.tools {
		tools = append(tools, map[string]any{
			"name":        name,
			"description": tool.Description(),
			"inputSchema": tool.InputSchema(),
		})
	}
	return tools
}

// CallTool executes a tool by name.
func (s *Server) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	s.toolsMu.RLock()
	tool, ok := s.tools[name]
	s.toolsMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return tool.Execute(ctx, args)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Enable CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		return
	}

	switch r.URL.Path {
	case "/sse":
		s.handleSSE(w, r)
	case "/message":
		s.handleMessage(w, r)
	case "/tools":
		s.handleToolsList(w, r)
	default:
		s.handleJSONRPC(w, r)
	}
}

func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.ListTools()); err != nil {
		log.Printf("Failed to encode tools list: %v", err)
	}
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sessionID := fmt.Sprintf("sess_%d", time.Now().UnixNano())
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

	// Send endpoint event
	fmt.Fprintf(w, "event: endpoint\ndata: /message?sessionId=%s\n\n", sessionID)
	flusher.Flush()

	log.Printf("📤 SSE client connected: %s", sessionID)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			log.Printf("📥 SSE client disconnected: %s", sessionID)
			return
		case msg := <-session.messages:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error")
		return
	}

	result, err := s.processRequest(r.Context(), &req)
	if err != nil {
		s.writeError(w, req.ID, err.Code, err.Message)
		return
	}

	if sessionID != "" {
		s.sessionsMu.RLock()
		session, ok := s.sessions[sessionID]
		s.sessionsMu.RUnlock()

		if ok {
			resp := Response{JSONRPC: "2.0", ID: req.ID, Result: result}
			respBytes, _ := json.Marshal(resp)
			select {
			case session.messages <- respBytes:
			case <-session.done:
				log.Printf("Session closed before message sent: %s", sessionID)
			case <-time.After(5 * time.Second):
				log.Printf("Timeout sending message to session: %s", sessionID)
			}
		}
	}

	s.writeResult(w, req.ID, result)
}

func (s *Server) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

type jsonRPCError struct {
	Code    int
	Message string
}

func (s *Server) processRequest(ctx context.Context, req *Request) (any, *jsonRPCError) {
	log.Printf("📨 Received: %s (id: %v)", req.Method, req.ID)

	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]any{
				"name":    "mcp-test-server",
				"version": "1.0.0",
			},
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
		}, nil

	case "tools/list":
		return map[string]any{"tools": s.ListTools()}, nil

	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &jsonRPCError{Code: -32602, Message: "invalid params"}
		}

		log.Printf("🔧 Calling tool: %s with args: %v", params.Name, params.Arguments)

		result, err := s.CallTool(ctx, params.Name, params.Arguments)
		if err != nil {
			log.Printf("❌ Tool error: %v", err)
			return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
		}

		log.Printf("✅ Tool result: %v", result)
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": mustMarshal(result)},
			},
		}, nil

	case "ping":
		return map[string]any{}, nil

	default:
		return nil, &jsonRPCError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) writeResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", ID: id, Result: result}); err != nil {
		log.Printf("Failed to encode result: %v", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &Error{Code: code, Message: message},
	}); err != nil {
		log.Printf("Failed to encode error: %v", err)
	}
}

func mustMarshal(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func main() {
	port := flag.Int("port", 8082, "Port to listen on")
	flag.Parse()

	server := NewServer()

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("🎲 MCP Test Server starting on http://%s", addr)
	log.Printf("📋 Available tools:")
	log.Printf("   - roll_dice: Roll dice and get random results")
	log.Printf("   - echo: Echo back messages with enthusiasm!")
	log.Printf("   - get_time: Get current time in various formats")
	log.Printf("")
	log.Printf("🔌 SSE endpoint: http://%s/sse", addr)
	log.Printf("🔌 JSON-RPC endpoint: http://%s/", addr)
	log.Printf("📋 Tools list: http://%s/tools", addr)
	log.Printf("")
	log.Printf("💡 Test with: curl http://%s/tools", addr)

	//nolint:gosec // G114: timeouts not needed for test server
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
