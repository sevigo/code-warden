package app

import (
	"log/slog"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/db"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
)

// App holds the main dependencies of the application.
type App struct {
	Cfg         *config.Config
	Store       storage.Store
	VectorStore storage.VectorStore
	RepoMgr     repomanager.RepoManager
	Dispatcher  core.JobDispatcher
	Logger      *slog.Logger
	DB          *db.DB
	RAGService  llm.RAGService
	Server      *server.Server
	GitClient   *gitutil.Client
}

// NewApp creates a new App instance.
func NewApp(
	cfg *config.Config,
	dbConn *db.DB,
	store storage.Store,
	vs storage.VectorStore,
	repoMgr repomanager.RepoManager,
	dispatcher core.JobDispatcher,
	rag llm.RAGService,
	srv *server.Server,
	gitClient *gitutil.Client,
	logger *slog.Logger,
) *App {
	logger.Info("initializing Code Warden application",
		"llm_provider", cfg.AI.LLMProvider,
		"embedder_provider", cfg.AI.EmbedderProvider,
		"generator_model", cfg.AI.GeneratorModel,
		"embedder_model", cfg.AI.EmbedderModel,
		"max_workers", cfg.Server.MaxWorkers,
		"repo_path", cfg.Storage.RepoPath,
	)

	return &App{
		Cfg:         cfg,
		DB:          dbConn,
		Store:       store,
		VectorStore: vs,
		RepoMgr:     repoMgr,
		Dispatcher:  dispatcher,
		RAGService:  rag,
		Server:      srv,
		GitClient:   gitClient,
		Logger:      logger,
	}
}

// Start runs the HTTP server.
func (a *App) Start() error {
	a.Logger.Info("application config",
		"port", a.Cfg.Server.Port,
		"max_workers", a.Cfg.Server.MaxWorkers,
	)
	if err := a.Server.Start(); err != nil {
		a.Logger.Error("failed to start HTTP server", "error", err)
		return err
	}

	return nil
}

// Stop shuts down the application cleanly.
func (a *App) Stop() error {
	var shutdownErr error
	a.Logger.Info("shutting down Code Warden services")

	// Stop the job dispatcher, allowing in-flight jobs to finish.
	a.Dispatcher.Stop()

	// Stop the HTTP server to prevent new incoming requests.
	if a.Server != nil {
		if err := a.Server.Stop(); err != nil {
			a.Logger.Error("error during HTTP server shutdown", "error", err)
			shutdownErr = err
		}
	}

	if shutdownErr != nil {
		a.Logger.Error("Code Warden stopped with errors", "error", shutdownErr)
	} else {
		a.Logger.Info("Code Warden stopped successfully")
	}
	return shutdownErr
}
