// Package server implements the HTTP server for the application.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
)

// Server wraps an HTTP server with graceful shutdown capabilities.
type Server struct {
	ctx    context.Context
	server *http.Server
	logger *slog.Logger
}

// NewServer creates a new HTTP server with the given configuration and job dispatcher.
func NewServer(ctx context.Context, cfg *config.Config, dispatcher core.JobDispatcher, logger *slog.Logger) *Server {
	router := NewRouter(cfg, dispatcher, logger)

	return &Server{
		ctx: ctx,
		server: &http.Server{
			Addr:         ":" + cfg.Server.Port,
			Handler:      router,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		logger: logger,
	}
}

// Start starts the HTTP server and blocks until shutdown or error.
func (s *Server) Start() error {
	s.logger.Info("starting HTTP server", "address", s.server.Addr)

	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server failed to start: %w", err)
	}
	return nil
}

// Stop gracefully shuts down the server with a 30-second timeout.
func (s *Server) Stop() error {
	s.logger.Info("shutting down HTTP server")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return s.server.Shutdown(shutdownCtx)
}
