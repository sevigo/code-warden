package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server/handler"
	"github.com/sevigo/code-warden/internal/storage"
)

// NewRouter creates and configures a new HTTP router with middleware and API routes.
func NewRouter(cfg *config.Config, dispatcher core.JobDispatcher, logger *slog.Logger) *chi.Mux {
	return NewRouterWithStore(cfg, dispatcher, nil, nil, nil, nil, nil, logger)
}

// NewRouterWithStore creates a router with storage for web UI endpoints.
func NewRouterWithStore(cfg *config.Config, dispatcher core.JobDispatcher, canceller core.SessionCanceller, store storage.Store, ragService rag.Service, repoMgr repomanager.RepoManager, gitClient *gitutil.Client, logger *slog.Logger) *chi.Mux {
	r := chi.NewRouter()

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
		webhookHandler := handler.NewWebhookHandler(cfg, dispatcher, canceller, logger)
		// Short timeout for webhook delivery acknowledgement
		r.With(middleware.Timeout(30*time.Second)).Post("/webhook/github", webhookHandler.Handle)

		// Web UI API routes
		if store != nil {
			webUIHandler := handler.NewWebUIHandler(store, ragService, repoMgr, gitClient, cfg, logger)
			dashboardHandler := handler.NewDashboardHandler(cfg, store, logger)

			// Fast endpoints — short timeout is fine
			r.With(middleware.Timeout(30*time.Second)).Get("/repos", webUIHandler.ListRepos)
			r.With(middleware.Timeout(30*time.Second)).Post("/repos", webUIHandler.RegisterRepo)
			r.With(middleware.Timeout(30*time.Second)).Get("/repos/{repoId}", webUIHandler.GetRepo)
			r.With(middleware.Timeout(30*time.Second)).Post("/repos/{repoId}/scan", webUIHandler.TriggerScan)
			r.With(middleware.Timeout(30*time.Second)).Get("/repos/{repoId}/status", webUIHandler.GetScanStatus)
			r.With(middleware.Timeout(30*time.Second)).Get("/repos/{repoId}/stats", webUIHandler.GetRepoStats)

			// LLM endpoints — 10 min timeout (Ollama can be slow)
			r.With(middleware.Timeout(10*time.Minute)).Post("/repos/{repoId}/chat", webUIHandler.Chat)
			r.With(middleware.Timeout(10*time.Minute)).Post("/repos/{repoId}/explain", webUIHandler.Explain)

			// SSE — no timeout, long-lived connection
			r.Get("/events", webUIHandler.SSEEvents)

			// Dashboard endpoints (mock data — wire to real services later)
			r.With(middleware.Timeout(30*time.Second)).Get("/setup/status", dashboardHandler.SetupStatus)
			r.With(middleware.Timeout(30*time.Second)).Get("/config", dashboardHandler.GetConfig)
			r.With(middleware.Timeout(30*time.Second)).Get("/stats/global", dashboardHandler.GlobalStats)
			r.With(middleware.Timeout(30*time.Second)).Get("/jobs", dashboardHandler.ListJobs)
			r.With(middleware.Timeout(30*time.Second)).Get("/repos/{repoId}/reviews", dashboardHandler.ListReviews)
			r.With(middleware.Timeout(30*time.Second)).Get("/repos/{repoId}/reviews/{prNumber}", dashboardHandler.GetReview)
			r.With(middleware.Timeout(30*time.Second)).Post("/repos/{repoId}/reviews/{prNumber}/feedback", dashboardHandler.SubmitFeedback)
		}
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

	return r
}
