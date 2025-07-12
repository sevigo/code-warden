// Package gitutil provides utilities for working with Git repositories,
// including cloning and checking out specific commits.
package gitutil

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// Cloner handles cloning Git repositories and checking out specific commits.
type Cloner struct {
	Logger *slog.Logger
}

// NewCloner returns a new Cloner instance.
// If logger is nil, the default slog logger is used.
func NewCloner(logger *slog.Logger) *Cloner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Cloner{Logger: logger}
}

// Clone clones the repository at repoURL into a temporary directory,
// checks out the given commit SHA, and returns the path along with a cleanup function.
func (c *Cloner) Clone(ctx context.Context, repoURL, sha string) (string, func(), error) {
	if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "http://") {
		return "", nil, fmt.Errorf("invalid repository URL: %s", repoURL)
	}

	repoPath, err := os.MkdirTemp("", "code-warden-repo-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Define the cleanup function. It captures the repoPath variable from the outer scope.
	cleanup := func() {
		c.Logger.Info("cleaning up temporary repository", "path", repoPath)
		if removeErr := os.RemoveAll(repoPath); removeErr != nil {
			c.Logger.Error("failed to remove temporary repository directory", "path", repoPath, "error", removeErr)
		}
	}

	c.Logger.InfoContext(ctx, "cloning repository", "url", repoURL, "path", repoPath, "sha", sha)

	repo, err := git.PlainCloneContext(ctx, repoPath, false, &git.CloneOptions{
		URL: repoURL,
	})
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to clone repo '%s': %w", repoURL, err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to get worktree for repo '%s': %w", repoURL, err)
	}

	c.Logger.InfoContext(ctx, "checking out commit", "sha", sha)

	err = worktree.Checkout(&git.CheckoutOptions{
		Hash:  plumbing.NewHash(sha),
		Force: true,
	})
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to checkout commit '%s': %w", sha, err)
	}

	c.Logger.InfoContext(ctx, "repository cloned and checked out")

	return repoPath, cleanup, nil
}
