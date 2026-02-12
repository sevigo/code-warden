// Package gitutil provides a client for working with Git repositories.
package gitutil

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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
	// Use git CLI to clone with longpaths enabled
	cmd := exec.CommandContext(ctx, "git", "-c", "core.longpaths=true", "clone", authURL, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %s: %w", string(out), err)
	}

	// Make sure we can open it with go-git for Diff operations later
	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open cloned repo: %w", err)
	}
	return repo, nil
}

// Fetch fetches updates from the 'origin' remote.
// Fetch fetches updates from the 'origin' remote using git CLI.
func (c *Client) Fetch(ctx context.Context, path string, _ string, refSpecs ...string) error {
	c.Logger.InfoContext(ctx, "fetching latest changes from origin")

	// Inject global config for longpaths
	args := []string{"-c", "core.longpaths=true", "fetch", "origin", "--force"}
	args = append(args, refSpecs...)

	// Retry logic for transient errors (e.g. 500 Internal Server Error)
	const maxRetries = 3
	const baseDelay = 2 * time.Second

	var err error
	for i := 0; i <= maxRetries; i++ {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = path

		// If this is not the first attempt, log a warning and wait
		if i > 0 {
			delay := baseDelay * time.Duration(1<<(i-1))
			c.Logger.WarnContext(ctx, "git fetch failed, retrying",
				"attempt", i,
				"max_retries", maxRetries,
				"delay", delay,
				"error", err,
			)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
			err = fmt.Errorf("git fetch failed: %s: %w", string(out), cmdErr)
			continue
		}

		// Success
		c.Logger.InfoContext(ctx, "fetch complete")
		return nil
	}

	return err
}

// Checkout switches the repository's worktree to a specific commit.
// Checkout switches the repository's worktree to a specific commit using git CLI.
func (c *Client) Checkout(ctx context.Context, path string, sha string) error {
	c.Logger.Info("checking out commit", "sha", sha)

	// Ensure we don't have lingering locks by using force, and enable longpaths
	cmd := exec.CommandContext(ctx, "git", "-c", "core.longpaths=true", "checkout", "--force", sha)
	cmd.Dir = path

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout failed: %s: %w", string(out), err)
	}
	return nil
}

// GetRemoteHeadSHA fetches the HEAD commit SHA of a specific remote branch without cloning.
func (c *Client) GetRemoteHeadSHA(repoURL, branch, token string) (string, error) {
	authURL, err := c.getAuthenticatedURL(repoURL, token)
	if err != nil {
		return "", err
	}

	// Use `git ls-remote` to get the ref for the specific branch.
	// `refs/heads/main` for example.
	ref := fmt.Sprintf("refs/heads/%s", branch)
	out, err := exec.CommandContext(context.Background(), "git", "ls-remote", authURL, ref).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote failed: %w. Ensure branch '%s' exists", err, branch)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return "", fmt.Errorf("branch '%s' not found or repository is empty", branch)
	}
	return strings.Fields(output)[0], nil
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

	_, err = c.Clone(ctx, repoURL, repoPath, token)
	if err != nil {
		cleanup()
		return "", nil, err // Error is already well-formatted
	}

	// repo is unused for Checkout now, but we need it to return for potential Open later if needed
	// or we just ignore it. Clone already returned it opened.

	if err := c.Checkout(ctx, repoPath, sha); err != nil {
		cleanup()
		return "", nil, err
	}

	c.Logger.InfoContext(ctx, "repository cloned and checked out successfully")
	return repoPath, cleanup, nil
}

func (c *Client) getAuthenticatedURL(repoURL, token string) (string, error) {
	// Handle local paths directly. file:// is intentionally unsupported for security.
	if !strings.Contains(repoURL, "://") {
		return repoURL, nil
	}

	if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "http://") {
		return "", fmt.Errorf("invalid repository URL: %s", repoURL)
	}

	parsedURL, err := url.Parse(repoURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse repository URL '%s': %w", repoURL, err)
	}
	parsedURL.User = url.UserPassword("x-access-token", token)
	return parsedURL.String(), nil
}

// GetHeadSHA returns the current HEAD SHA of the repository at the given path.
func (c *Client) GetHeadSHA(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-c", "core.longpaths=true", "rev-parse", "HEAD")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
