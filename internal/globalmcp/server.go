// Package globalmcp provides a global MCP server that runs continuously
// alongside the main application server, providing agent tools for CLI
// and external integrations.
package globalmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sevigo/code-warden/internal/config"
)

const (
	defaultWriteTimeout = 30 * time.Minute
	defaultIdleTimeout  = 120 * time.Second
)

type Server struct {
	addr        string
	logger      *slog.Logger
	httpServer  *http.Server
	ready       chan struct{}
	readyOnce   sync.Once
	startupOnce sync.Once
	mu          sync.RWMutex
}

func NewServer(cfg *config.Config, logger *slog.Logger) *Server {
	return &Server{
		addr:   cfg.Agent.MCPAddr,
		logger: logger,
		ready:  make(chan struct{}),
	}
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
	return server.Shutdown(ctx)
}

// handleHealth provides a health check endpoint.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "code-warden-mcp",
	}); err != nil {
		s.logger.Error("failed to encode health response", "error", err)
	}
}

// handleSSE handles SSE connections for MCP protocol.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Get workspace from query
	workspace := r.URL.Query().Get("workspace")
	if workspace == "" {
		workspace = "default"
	}

	// Send initial endpoint event
	fmt.Fprintf(w, "event: endpoint\ndata: http://%s/message?workspace=%s\n\n", s.addr, workspace)

	flusher, ok := w.(http.Flusher)
	if ok {
		flusher.Flush()
	}

	// Keep connection alive
	ctx := r.Context()
	<-ctx.Done()
}

// handleMessage handles MCP tool calls.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"content": []map[string]string{
			{
				"type": "text",
				"text": "Global MCP server is running. Repository-specific tools require a job context. Start an implementation job with /implement to access the full tool set.",
			},
		},
	}); err != nil {
		s.logger.Error("failed to encode message response", "error", err)
	}
}
