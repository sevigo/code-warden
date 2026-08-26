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
	"github.com/sevigo/code-warden/internal/storage"
)

// NewRouterWithStore creates a router for webhook and setup endpoints.
func NewRouterWithStore(cfg *config.Config, dispatcher core.JobDispatcher, store storage.Store, logger *slog.Logger) (*chi.Mux, *handler.SetupHandler) {
	r := chi.NewRouter()
	var setupHandler *handler.SetupHandler

	// Configure middleware stack
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Health check endpoint
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		webhookHandler := handler.NewWebhookHandler(cfg, dispatcher, logger)
		// Short timeout for webhook delivery acknowledgement
		r.With(middleware.Timeout(30*time.Second)).Post("/webhook/github", webhookHandler.Handle)

		// Setup wizard endpoints — mounted unconditionally so the wizard is
		// reachable even before storage is wired (first-boot scenario).
		// The handlers themselves return 503 if the credential store is nil.
		setupHandler = handler.NewSetupHandler(cfg, logger)
		r.With(middleware.Timeout(30*time.Second)).Post("/setup/github/manifest", setupHandler.GitHubManifest)
		r.With(middleware.Timeout(30*time.Second)).Get("/setup/github/callback", setupHandler.GitHubCallback)
		r.With(middleware.Timeout(10*time.Second)).Post("/setup/credentials", setupHandler.SaveCredentials)
		r.With(middleware.Timeout(10*time.Second)).Post("/setup/test-llm", setupHandler.TestLLM)
		r.With(middleware.Timeout(10*time.Second)).Post("/setup/test-webhook", setupHandler.TestWebhook)

		// Setup wizard endpoints — mounted unconditionally so the wizard is
		// reachable even before storage is wired (first-boot scenario).
		// The handlers themselves return 503 if the credential store is nil.
		setupHandler = handler.NewSetupHandler(cfg, logger)
		if store != nil {
			setupHandler.SetStore(store)
		}
		r.With(middleware.Timeout(30*time.Second)).Get("/setup/status", setupHandler.SetupStatus)
		r.With(middleware.Timeout(30*time.Second)).Post("/setup/github/manifest", setupHandler.GitHubManifest)
		r.With(middleware.Timeout(30*time.Second)).Get("/setup/github/callback", setupHandler.GitHubCallback)
		r.With(middleware.Timeout(10*time.Second)).Post("/setup/credentials", setupHandler.SaveCredentials)
		r.With(middleware.Timeout(10*time.Second)).Post("/setup/test-llm", setupHandler.TestLLM)
		r.With(middleware.Timeout(10*time.Second)).Post("/setup/test-webhook", setupHandler.TestWebhook)
	})

	// Serve static UI files (built React app)
	if store != nil {
		fs := http.FileServer(http.Dir("./ui/dist"))
		r.Handle("/assets/*", fs)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "./ui/dist/index.html")
		})
		// SPA fallback - serve index.html for unmatched routes
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			// Don't fallback for API routes
			if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, "./ui/dist/index.html")
		})
	}

	return r, setupHandler
}
