// Package gitutil provides utilities for working with Git repositories,
// including cloning and checking out specific commits.
package gitutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Cloner handles cloning Git repositories and checking out specific commits.
type Cloner struct {
	Logger *slog.Logger
}

// NewCloner returns a new Cloner instance.
func NewCloner(logger *slog.Logger) *Cloner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Cloner{Logger: logger}
}

// Clone clones the repository at repoURL into a temporary directory,
// checks out the given commit SHA, and returns the path along with a cleanup function.
func (c *Cloner) Clone(ctx context.Context, repoURL, sha, token string) (string, func(), error) {
	if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "http://") {
		return "", nil, fmt.Errorf("invalid repository URL: %s", repoURL)
	}
	if token == "" {
		return "", nil, fmt.Errorf("github token cannot be empty for cloning")
	}

	parsedURL, err := url.Parse(repoURL)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse repository URL '%s': %w", repoURL, err)
	}
	// Set the user information for authentication. GitHub Apps use 'x-access-token'.
	parsedURL.User = url.UserPassword("x-access-token", token)
	authenticatedURL := parsedURL.String()

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
		URL: authenticatedURL,
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

// Pull updates the repository at repoPath to the given SHA.
func (c *Cloner) Pull(ctx context.Context, repoPath, sha, token string) error {
	if token == "" {
		return fmt.Errorf("github token cannot be empty for pulling")
	}

	c.Logger.InfoContext(ctx, "pulling latest changes", "path", repoPath, "sha", sha)

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository at %s: %w", repoPath, err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree for repo %s: %w", repoPath, err)
	}

	// Fetch the latest changes from the remote
	err = worktree.PullContext(ctx, &git.PullOptions{
		RemoteName: "origin",
		Auth: &githttp.BasicAuth{
			Username: "x-access-token",
			Password: token,
		},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("failed to pull latest changes for repo %s: %w", repoPath, err)
	}

	// Checkout the specific SHA
	c.Logger.InfoContext(ctx, "checking out commit", "sha", sha)
	err = worktree.Checkout(&git.CheckoutOptions{
		Hash:  plumbing.NewHash(sha),
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("failed to checkout commit %s for repo %s: %w", sha, repoPath, err)
	}

	return nil
}

// Diff calculates the difference between two SHAs and returns lists of added, modified, and deleted files.
func (c *Cloner) Diff(ctx context.Context, repoPath, oldSHA, newSHA string) (added, modified, deleted []string, err error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open repository at %s: %w", repoPath, err)
	}

	// Get the commit objects for oldSHA and newSHA
	oldCommit, err := repo.CommitObject(plumbing.NewHash(oldSHA))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get commit object for old SHA %s: %w", oldSHA, err)
	}
	newCommit, err := repo.CommitObject(plumbing.NewHash(newSHA))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get commit object for new SHA %s: %w", newSHA, err)
	}

	// Get the tree objects for the commits
	oldTree, err := oldCommit.Tree()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get tree for old commit %s: %w", oldSHA, err)
	}
	newTree, err := newCommit.Tree()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get tree for new commit %s: %w", newSHA, err)
	}

	// Compare the trees
	changes, err := object.DiffTree(oldTree, newTree)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get diff between %s and %s: %w", oldSHA, newSHA, err)
	}

	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			c.Logger.ErrorContext(ctx, "failed to get action for change", "error", err)
			continue
		}

		switch action.String() {
		case "Add":
			added = append(added, change.To.Name)
		case "Modify":
			modified = append(modified, change.To.Name)
		case "Delete":
			deleted = append(deleted, change.From.Name)
		}
	}

	return added, modified, deleted, nil
}
