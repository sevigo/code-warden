package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/server/handler"
)

// NewRouter creates and configures a new HTTP router with middleware and API routes.
func NewRouter(cfg *config.Config, dispatcher core.JobDispatcher, logger *slog.Logger) *chi.Mux {
	r := chi.NewRouter()

	// Configure middleware stack
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// Health check endpoint
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		webhookHandler := handler.NewWebhookHandler(cfg, dispatcher, logger)
		r.Post("/webhook/github", webhookHandler.Handle)
	})

	return r
}
