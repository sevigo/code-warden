package main

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

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

func scanRepoCmd(app *app.App, path, repoFullName string, force bool) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		updateResult, err := app.RepoMgr.ScanLocalRepo(ctx, path, repoFullName, force)
		if err != nil {
			return errorMsg{err}
		}

		repoConfig := core.DefaultRepoConfig()

		repoRecord, err := app.RepoMgr.GetRepoRecord(ctx, updateResult.RepoFullName)
		if err != nil {
			return errorMsg{err}
		}
		collectionName := repoRecord.QdrantCollectionName

		if updateResult.IsInitialClone {
			err = app.RAGService.SetupRepoContext(ctx, repoConfig, collectionName, updateResult.RepoPath)
		} else if len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0 {
			err = app.RAGService.UpdateRepoContext(ctx, repoConfig, collectionName, updateResult.RepoPath, updateResult.FilesToAddOrUpdate, updateResult.FilesToDelete)
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

func answerQuestionCmd(app *app.App, collectionName, question string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		answer, err := app.RAGService.AnswerQuestion(ctx, collectionName, question)
		if err != nil {
			return errorMsg{err}
		}
		return answerChunkMsg(answer)
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
			LastIndexedSHA:       "", // Not indexed yet
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
		if err != nil {
			return errorMsg{err}
		}
		return reposLoadedMsg{repos: repos}
	}
}
