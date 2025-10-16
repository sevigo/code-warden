package main

import (
	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/storage"
)

// Indicates that the core application services have been initialized.
type appInitializedMsg struct {
	app *app.App
	err error
}

// Indicates that a repository scan has completed.
type scanCompleteMsg struct {
	repoPath       string
	repoFullName   string
	collectionName string
	err            error
}

type repoAddedMsg struct {
	repoFullName string
	repoPath     string
	err          error
}

// Represents a complete, non-streaming answer from the LLM.
type answerCompleteMsg struct{ content string }

// A generic error message for reporting failures from commands.
type errorMsg struct{ err error }

func (e errorMsg) Error() string {
	return e.err.Error()
}

type reposLoadedMsg struct {
	repos []*storage.Repository
	err   error
}
