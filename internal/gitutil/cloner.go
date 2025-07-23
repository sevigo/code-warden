// Package gitutil provides a client for working with Git repositories.
package gitutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// Client handles interacting with Git repositories.
type Client struct {
	Logger *slog.Logger
}

// NewClient returns a new Client instance.
func NewClient(logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{Logger: logger}
}

// Open opens a Git repository at a given path.
func (c *Client) Open(path string) (*git.Repository, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository at %s: %w", path, err)
	}
	return repo, nil
}

// Clone clones a repository to a specific path. It does not checkout a specific SHA.
func (c *Client) Clone(ctx context.Context, repoURL, path, token string) (*git.Repository, error) {
	authURL, err := c.getAuthenticatedURL(repoURL, token)
	if err != nil {
		return nil, err
	}

	c.Logger.InfoContext(ctx, "cloning repository", "url", repoURL, "path", path)
	repo, err := git.PlainCloneContext(ctx, path, false, &git.CloneOptions{
		URL: authURL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to clone repo '%s' to '%s': %w", repoURL, path, err)
	}
	return repo, nil
}

// Fetch fetches updates from the 'origin' remote.
func (c *Client) Fetch(ctx context.Context, repo *git.Repository, token string) error {
	c.Logger.InfoContext(ctx, "fetching latest changes from origin")
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       c.getBasicAuth(token),
		Force:      true, // Allow fetching unrelated histories
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("failed to fetch from remote: %w", err)
	}
	c.Logger.InfoContext(ctx, "fetch complete")
	return nil
}

// Checkout switches the repository's worktree to a specific commit.
func (c *Client) Checkout(repo *git.Repository, sha string) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	c.Logger.Info("checking out commit", "sha", sha)
	err = worktree.Checkout(&git.CheckoutOptions{
		Hash:  plumbing.NewHash(sha),
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("failed to checkout commit '%s': %w", sha, err)
	}
	return nil
}

// Diff calculates the difference between two SHAs in an open repository.
func (c *Client) Diff(repo *git.Repository, oldSHA, newSHA string) (added, modified, deleted []string, err error) {
	// Get commit objects
	oldCommit, err := repo.CommitObject(plumbing.NewHash(oldSHA))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get commit object for old SHA %s: %w", oldSHA, err)
	}
	newCommit, err := repo.CommitObject(plumbing.NewHash(newSHA))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get commit object for new SHA %s: %w", newSHA, err)
	}

	// Get tree objects
	oldTree, err := oldCommit.Tree()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get tree for old commit %s: %w", oldSHA, err)
	}
	newTree, err := newCommit.Tree()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get tree for new commit %s: %w", newSHA, err)
	}

	// Compare trees
	changes, err := object.DiffTree(oldTree, newTree)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to diff trees between %s and %s: %w", oldSHA, newSHA, err)
	}

	// Process changes
	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			c.Logger.Error("failed to get action for change, skipping", "error", err)
			continue
		}

		switch action {
		case merkletrie.Insert:
			added = append(added, change.To.Name)
		case merkletrie.Modify:
			modified = append(modified, change.To.Name)
		case merkletrie.Delete:
			deleted = append(deleted, change.From.Name)
		}
	}
	return added, modified, deleted, nil
}

// CloneAndCheckoutTemp clones a repo into a temporary directory, checks out a commit,
// and returns the path with a cleanup function. This preserves the original Cloner.Clone functionality.
func (c *Client) CloneAndCheckoutTemp(ctx context.Context, repoURL, sha, token string) (string, func(), error) {
	repoPath, err := os.MkdirTemp("", "code-warden-repo-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	cleanup := func() {
		c.Logger.Info("cleaning up temporary repository", "path", repoPath)
		if removeErr := os.RemoveAll(repoPath); removeErr != nil {
			c.Logger.Error("failed to remove temp repo", "path", repoPath, "error", removeErr)
		}
	}

	repo, err := c.Clone(ctx, repoURL, repoPath, token)
	if err != nil {
		cleanup()
		return "", nil, err // Error is already well-formatted
	}

	if err := c.Checkout(repo, sha); err != nil {
		cleanup()
		return "", nil, err // Error is already well-formatted
	}

	c.Logger.InfoContext(ctx, "repository cloned and checked out successfully")
	return repoPath, cleanup, nil
}

func (c *Client) getAuthenticatedURL(repoURL, token string) (string, error) {
	if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "http://") {
		return "", fmt.Errorf("invalid repository URL: %s", repoURL)
	}
	if token == "" {
		return "", errors.New("github token cannot be empty")
	}

	parsedURL, err := url.Parse(repoURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse repository URL '%s': %w", repoURL, err)
	}
	parsedURL.User = url.UserPassword("x-access-token", token)
	return parsedURL.String(), nil
}

func (c *Client) getBasicAuth(token string) *githttp.BasicAuth {
	if token == "" {
		return nil
	}
	return &githttp.BasicAuth{
		Username: "x-access-token",
		Password: token,
	}
}

// GetRemoteHeadSHA fetches the latest commit SHA for a given branch from a remote repository.
// It uses `git ls-remote` to avoid a full clone.
func (c *Client) GetRemoteHeadSHA(repoURL, branch, token string) (string, error) {
	cmd := exec.Command("git", "ls-remote", repoURL, branch)

	// Set up authentication for private repositories
	if token != "" {
		// For HTTPS, git uses a credential helper or expects auth in URL
		// For simplicity, we'll embed the token in the URL for ls-remote if it's HTTPS
		if strings.HasPrefix(repoURL, "https://") {
			parsedURL, err := url.Parse(repoURL)
			if err != nil {
				return "", fmt.Errorf("failed to parse repo URL: %w", err)
			}
			parsedURL.User = url.UserPassword("x-access-token", token)
			cmd = exec.Command("git", "ls-remote", parsedURL.String(), branch)
		} else {
			// For SSH, git typically uses SSH keys, not tokens in this manner.
			// This case might need more complex handling or be out of scope.
			c.Logger.Warn("git ls-remote with token for non-HTTPS URL might not work as expected", "url", repoURL)
		}
	}

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git ls-remote failed with exit code %d: %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("failed to run git ls-remote: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("git ls-remote returned no output for %s branch %s", repoURL, branch)
	}

	// The first part of the line is the SHA, followed by a tab and the ref name
	sha := strings.Fields(lines[0])[0]
	return sha, nil
}
