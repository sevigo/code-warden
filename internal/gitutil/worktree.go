package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrPRHeadSuperseded indicates the pull request's head moved to a new commit
// between when a job was created and when its workspace was built. Callers
// must stop rather than review a different commit than the one intended.
var ErrPRHeadSuperseded = errors.New("pull request head was superseded by a newer commit")

// FetchPRHeadWorktree fetches the pull request's head ref
// (refs/pull/<prNumber>/head) into repoPath — an existing clone, typically a
// repository's kept-up-to-date default-branch clone — and creates a detached
// worktree checked out at headSHA. This avoids a fresh network clone per job:
// only the single PR ref is fetched into a clone that already exists locally.
//
// If the fetched ref's tip does not equal headSHA, the PR moved to a new
// commit after the caller decided to review headSHA; this returns
// ErrPRHeadSuperseded rather than silently reviewing the wrong commit.
//
// repoPath is a shared, mutable resource: concurrent Fetch/worktree-add calls
// against the same repoPath can corrupt git's on-disk state (index locks,
// packed-refs), so callers must hold a per-repository lock around this call.
// The returned cleanup removes the worktree; it does not touch repoPath.
func (c *Client) FetchPRHeadWorktree(ctx context.Context, repoPath string, prNumber int, headSHA, token string) (string, func(), error) {
	localRef := fmt.Sprintf("refs/code-warden/pr/%d", prNumber)
	refSpec := fmt.Sprintf("+refs/pull/%d/head:%s", prNumber, localRef)
	if err := c.Fetch(ctx, repoPath, token, refSpec); err != nil {
		return "", nil, fmt.Errorf("failed to fetch PR #%d head: %w", prNumber, err)
	}

	tip, err := c.revParse(ctx, repoPath, localRef)
	if err != nil {
		return "", nil, fmt.Errorf("failed to resolve fetched PR #%d head: %w", prNumber, err)
	}
	if tip != headSHA {
		return "", nil, fmt.Errorf("%w: fetched %s, expected %s", ErrPRHeadSuperseded, tip, headSHA)
	}

	worktreePath, err := os.MkdirTemp("", "code-warden-pr-worktree-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp worktree directory: %w", err)
	}

	if err := c.WorktreeAdd(ctx, repoPath, worktreePath, headSHA); err != nil {
		_ = os.RemoveAll(worktreePath)
		return "", nil, fmt.Errorf("failed to create worktree for PR #%d head %s: %w", prNumber, headSHA, err)
	}

	cleanup := func() {
		c.Logger.Info("removing PR head worktree", "path", worktreePath)
		// A fresh, independent context: this runs from a defer, potentially
		// after the request context that drove the review has been canceled.
		if err := c.WorktreeRemove(context.Background(), repoPath, worktreePath); err != nil {
			c.Logger.Error("failed to remove worktree via git, falling back to directory removal",
				"path", worktreePath, "error", err)
			if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
				c.Logger.Error("failed to remove worktree directory", "path", worktreePath, "error", rmErr)
			}
		}
	}

	c.Logger.InfoContext(ctx, "created PR head worktree", "pr", prNumber, "sha", headSHA, "path", worktreePath)
	return worktreePath, cleanup, nil
}

// WorktreeAdd creates a detached worktree at worktreePath, checked out at sha,
// linked to the repository at repoPath.
func (c *Client) WorktreeAdd(ctx context.Context, repoPath, worktreePath, sha string) error {
	cmd := exec.CommandContext(ctx, "git", "-c", "core.longpaths=true", "worktree", "add", "--detach", worktreePath, sha)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WorktreeRemove removes the worktree at worktreePath from the repository at
// repoPath. Removal is forced: the worktree is disposable and nothing in it
// is written back to repoPath.
func (c *Client) WorktreeRemove(ctx context.Context, repoPath, worktreePath string) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", worktreePath)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WorktreePrune removes administrative metadata for worktrees whose
// directories no longer exist on disk — left behind when a crash skipped
// WorktreeRemove. Safe to call at startup before any worktree is created.
func (c *Client) WorktreePrune(ctx context.Context, repoPath string) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "prune")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree prune failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// revParse resolves ref to its commit SHA in the repository at path.
func (c *Client) revParse(ctx context.Context, path, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", ref)
	cmd.Dir = path
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s failed: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}
