package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/db"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/globalmcp"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/server"
	"github.com/sevigo/code-warden/internal/storage"
)

// App holds the main dependencies of the application.
type App struct {
	Cfg             *config.Config
	Store           storage.Store
	RepoMgr         repomanager.RepoManager
	Dispatcher      core.JobDispatcher
	Logger          *slog.Logger
	DB              *db.DB
	CredentialStore *config.CredentialStore
	Server          *server.Server
	GitClient       *gitutil.Client
	MCPServer       *globalmcp.Server
}

// NewApp creates a new App instance.
func NewApp(
	cfg *config.Config,
	dbConn *db.DB,
	store storage.Store,
	repoMgr repomanager.RepoManager,
	dispatcher core.JobDispatcher,
	srv *server.Server,
	gitClient *gitutil.Client,
	mcpServer *globalmcp.Server,
	logger *slog.Logger,
) *App {
	logger.Info("initializing Code Warden application",
		"llm_provider", cfg.AI.LLMProvider,
		"generator_model", cfg.AI.GeneratorModel,
		"max_workers", cfg.Server.MaxWorkers,
		"repo_path", cfg.Storage.RepoPath,
	)

	return &App{
		Cfg:        cfg,
		DB:         dbConn,
		Store:      store,
		RepoMgr:    repoMgr,
		Dispatcher: dispatcher,
		Server:     srv,
		GitClient:  gitClient,
		MCPServer:  mcpServer,
		Logger:     logger,
	}
}

// LoadCredentials creates a CredentialStore from the DB connection and loads
// any stored credentials into the config. Called after Wire init, before server start.
func (a *App) LoadCredentials() {
	cs, err := config.NewCredentialStore(a.DB.DB)
	if err != nil {
		a.Logger.Warn("credential store unavailable, falling back to config files", "error", err)
		return
	}
	a.CredentialStore = cs

	ctx := context.Background()
	var github config.GitHubAppCredentials
	if ok, err := cs.Load(ctx, "github_app", &github); ok {
		a.Cfg.ApplyDBCredentials(&github, nil)
		a.Logger.Info("loaded GitHub credentials from database")
	} else if err != nil {
		a.Logger.Warn("failed to load GitHub credentials from database", "error", err)
	}

	var llm config.LLMCredentials
	if ok, err := cs.Load(ctx, "llm", &llm); ok {
		a.Cfg.ApplyDBCredentials(nil, &llm)
		a.Logger.Info("loaded LLM credentials from database")
	} else if err != nil {
		a.Logger.Warn("failed to load LLM credentials from database", "error", err)
	}
}

// Start runs the HTTP server and MCP server.
func (a *App) Start() error {
	a.Logger.Info("application config",
		"port", a.Cfg.Server.Port,
		"max_workers", a.Cfg.Server.MaxWorkers,
	)

	// Start MCP server if configured
	if a.MCPServer != nil {
		if err := a.MCPServer.Start(context.Background()); err != nil {
			a.Logger.Error("failed to start MCP server", "error", err)
			return err
		}
	}

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

	// Stop MCP server with timeout
	if a.MCPServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := a.MCPServer.Stop(ctx)
		cancel()
		if err != nil {
			a.Logger.Error("error during MCP server shutdown", "error", err)
			shutdownErr = err
		}
	}

	// Stop the job dispatcher, allowing in-flight jobs to finish.
	a.Dispatcher.Stop()

	// Stop the HTTP server to prevent new incoming requests.
	if a.Server != nil {
		if err := a.Server.Stop(); err != nil {
			a.Logger.Error("error during HTTP server shutdown", "error", err)
			shutdownErr = a.firstError(shutdownErr, err)
		}
	}

	// Clear repository locks to free memory.
	if a.RepoMgr != nil {
		a.RepoMgr.ClearLocks()
	}

	if shutdownErr != nil {
		a.Logger.Error("Code Warden stopped with errors", "error", shutdownErr)
	} else {
		a.Logger.Info("Code Warden stopped successfully")
	}
	return shutdownErr
}

// firstError returns the first error if err1 is not nil, otherwise returns err2.
func (a *App) firstError(err1, err2 error) error {
	if err1 != nil {
		return err1
	}
	return err2
}
