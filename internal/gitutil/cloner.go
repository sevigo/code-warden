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
	// Use git CLI to clone with longpaths enabled and credential helper disabled to avoid Keychain prompts/conflicts
	cmd := exec.CommandContext(ctx, "git", "-c", "core.longpaths=true", "-c", "credential.helper=", "clone", authURL, path)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %s: %w", c.maskToken(string(out), token), err)
	}

	// Make sure we can open it with go-git for Diff operations later
	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open cloned repo: %w", err)
	}
	return repo, nil
}

// Fetch fetches updates from the 'origin' remote using git CLI.
func (c *Client) Fetch(ctx context.Context, path string, token string, refSpecs ...string) error {
	c.Logger.InfoContext(ctx, "fetching latest changes from origin")

	repoURL, err := c.getRemoteURL(ctx, path, "origin")
	if err != nil {
		c.Logger.WarnContext(ctx, "failed to get remote URL for authenticated fetch, trying as-is", "error", err)
	}

	authURL, err := c.getAuthenticatedURL(repoURL, token)
	if err != nil {
		return err
	}

	// args for fetch: use origin then the authURL if provided
	args := []string{"-c", "core.longpaths=true", "-c", "credential.helper=", "fetch", authURL, "--force"}
	args = append(args, refSpecs...)

	// Retry logic for transient errors (e.g. 500 Internal Server Error)
	const maxRetries = 3
	const baseDelay = 2 * time.Second

	for i := 0; i <= maxRetries; i++ {
		// Check context cancellation
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = path
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

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
			err = fmt.Errorf("git fetch failed: %s: %w", c.maskToken(string(out), token), cmdErr)
			continue
		}

		// Success
		c.Logger.InfoContext(ctx, "fetch complete")
		return nil
	}

	return err
}

func (c *Client) maskToken(input, token string) string {
	if token == "" {
		return input
	}
	return strings.ReplaceAll(input, token, "[MASKED]")
}

func (c *Client) getRemoteURL(ctx context.Context, path, remoteName string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", remoteName)
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Checkout switches the repository's worktree to a specific commit using git CLI.
func (c *Client) Checkout(ctx context.Context, path string, sha string) error {
	c.Logger.Info("checking out commit", "sha", sha)

	// Ensure we don't have lingering locks by using force, and enable longpaths
	cmd := exec.CommandContext(ctx, "git", "-c", "core.longpaths=true", "checkout", "--force", sha)
	cmd.Dir = path
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout failed: %s: %w", string(out), err)
	}
	return nil
}

// ResetToUpstream forcefully resets the current branch to match its remote tracking branch.
// This is necessary because Fetch only updates remote refs (like origin/main), while the
// local working tree pointer remains unchanged until specifically moved via merge or reset.
func (c *Client) ResetToUpstream(ctx context.Context, path string) error {
	c.Logger.InfoContext(ctx, "resetting worktree to upstream tracking branch")

	cmd := exec.CommandContext(ctx, "git", "-c", "core.longpaths=true", "reset", "--hard", "@{u}")
	cmd.Dir = path
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset --hard @{u} failed: %s: %w", string(out), err)
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
	// Handle local paths. file:// is explicitly blocked for security (SSRF risk).
	if strings.HasPrefix(strings.ToLower(repoURL), "file://") {
		return "", fmt.Errorf("file:// URLs are not supported for security reasons")
	}
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

	// Only add authentication if token is provided
	if token != "" {
		// Use x-access-token for all types of GitHub tokens.
		// This explicitly tells GitHub the "password" is a token, which is the most reliable way.
		parsedURL.User = url.UserPassword("x-access-token", token)
	}
	return parsedURL.String(), nil
}

// GetHeadSHA returns the current HEAD SHA of the repository at the given path.
func (c *Client) GetHeadSHA(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-c", "core.longpaths=true", "rev-parse", "HEAD")
	cmd.Dir = path
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
