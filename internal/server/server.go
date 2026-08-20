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
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server/handler"
	"github.com/sevigo/code-warden/internal/storage"
)

// Server wraps an HTTP server with graceful shutdown capabilities.
type Server struct {
	ctx              context.Context
	server           *http.Server
	logger           *slog.Logger
	dashboardHandler *handler.DashboardHandler
	setupHandler     *handler.SetupHandler
}

// SetCredentialStore injects the credential store into the setup and dashboard handlers.
func (s *Server) SetCredentialStore(cs *config.CredentialStore) {
	if s.dashboardHandler != nil {
		s.dashboardHandler.SetCredentialStore(cs)
	}
	if s.setupHandler != nil {
		s.setupHandler.SetCredentialStore(cs)
	}
}

// NewServerWithStore creates a new HTTP server with storage for web UI endpoints.
func NewServerWithStore(ctx context.Context, cfg *config.Config, dispatcher core.JobDispatcher, store storage.Store, repoMgr repomanager.RepoManager, gitClient *gitutil.Client, logger *slog.Logger) *Server {
	router, dashboardHandler, setupHandler := NewRouterWithStore(cfg, dispatcher, store, repoMgr, gitClient, logger)

	return &Server{
		ctx: ctx,
		server: &http.Server{
			Addr:         ":" + cfg.Server.Port,
			Handler:      router,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 15 * time.Minute,
			IdleTimeout:  120 * time.Second,
		},
		logger:           logger,
		dashboardHandler: dashboardHandler,
		setupHandler:     setupHandler,
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
