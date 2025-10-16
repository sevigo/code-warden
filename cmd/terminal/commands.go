package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert/yaml"

	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/code-warden/internal/wire"
)

func initializeAppCmd() tea.Cmd {
	return func() tea.Msg {
		app, cleanup, err := wire.InitializeApp(context.Background())
		if err != nil {
			return appInitializedMsg{err: err}
		}

		if err := app.Cfg.ValidateForCLI(); err != nil {
			cleanup()
			return appInitializedMsg{err: fmt.Errorf("cli configuration validation failed: %w", err)}
		}

		return appInitializedMsg{app: app}
	}
}

var (
	ErrConfigNotFound = errors.New("config file not found")
	ErrConfigParsing  = errors.New("config parsing failed")
)

// loadRepoConfig loads and parses the .code-warden.yml file from a repository path.
func loadRepoConfig(repoPath string) (*core.RepoConfig, error) {
	configPath := filepath.Join(repoPath, ".code-warden.yml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return core.DefaultRepoConfig(), ErrConfigNotFound
		}
		return nil, fmt.Errorf("failed to read .code-warden.yml: %w", err)
	}
	config := core.DefaultRepoConfig()
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfigParsing, err)
	}
	return config, nil
}

func scanRepoCmd(app *app.App, path, repoFullName string, force bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		updateResult, err := app.RepoMgr.ScanLocalRepo(ctx, path, repoFullName, force)
		if err != nil {
			return errorMsg{err}
		}

		repoConfig, err := loadRepoConfig(updateResult.RepoPath)
		if err != nil {
			if os.IsNotExist(err) {
				app.Logger.Info("no .code-warden.yml found, using defaults", "repo", updateResult.RepoFullName)
			} else {
				app.Logger.Warn("failed to parse .code-warden.yml, using defaults", "error", err, "repo", updateResult.RepoFullName)
			}
			repoConfig = core.DefaultRepoConfig()
		}

		repoRecord, err := app.RepoMgr.GetRepoRecord(ctx, updateResult.RepoFullName)
		if err != nil {
			return errorMsg{err}
		}
		collectionName := repoRecord.QdrantCollectionName

		if updateResult.IsInitialClone {
			err = app.RAGService.SetupRepoContext(
				ctx,
				repoConfig,
				collectionName,
				repoRecord.EmbedderModelName,
				updateResult.RepoPath,
			)
		} else if len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0 {
			err = app.RAGService.UpdateRepoContext(
				ctx,
				repoConfig,
				repoRecord,
				updateResult.RepoPath,
				updateResult.FilesToAddOrUpdate,
				updateResult.FilesToDelete,
			)
		}
		if err != nil {
			return errorMsg{err}
		}
		if err := app.RepoMgr.UpdateRepoSHA(ctx, updateResult.RepoFullName, updateResult.HeadSHA); err != nil {
			return errorMsg{err}
		}
		return scanCompleteMsg{
			repoPath:       path,
			repoFullName:   updateResult.RepoFullName,
			collectionName: collectionName,
		}
	}
}

func answerQuestionCmd(app *app.App, collectionName, embedderModelName, question string, history []string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		answer, err := app.RAGService.AnswerQuestion(ctx, collectionName, embedderModelName, question, history)
		if err != nil {
			return errorMsg{err}
		}
		return answerCompleteMsg{content: answer}
	}
}

func addRepoCmd(app *app.App, fullName, path string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		existingRepo, err := app.Store.GetRepositoryByFullName(ctx, fullName)
		if err != nil {
			return repoAddedMsg{err: fmt.Errorf("failed to check for existing repository: %w", err)}
		}
		if existingRepo != nil {
			return repoAddedMsg{err: fmt.Errorf("repository '%s' is already registered", fullName)}
		}
		collectionName := repomanager.GenerateCollectionName(fullName, app.Cfg.EmbedderModelName)
		newRepo := &storage.Repository{
			FullName:             fullName,
			ClonePath:            path,
			QdrantCollectionName: collectionName,
			EmbedderModelName:    app.Cfg.EmbedderModelName,
		}
		if err := app.Store.CreateRepository(ctx, newRepo); err != nil {
			return repoAddedMsg{err: fmt.Errorf("failed to create repository record: %w", err)}
		}
		return repoAddedMsg{repoFullName: fullName, repoPath: path}
	}
}

func loadReposCmd(app *app.App) tea.Cmd {
	return func() tea.Msg {
		repos, err := app.Store.GetAllRepositories(context.Background())
		return reposLoadedMsg{repos: repos, err: err}
	}
}
