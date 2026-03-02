package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/sevigo/code-warden/internal/gitutil"
)

// PushBranch pushes a local branch to the remote repository.
type PushBranch struct {
	ProjectRoot string
	Logger      *slog.Logger
}

func (t *PushBranch) Name() string {
	return "push_branch"
}

func (t *PushBranch) Description() string {
	return `Push current local changes to a remote branch.
You MUST call this before create_pull_request to ensure the remote branch exists.`
}

func (t *PushBranch) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"branch": map[string]any{
				"type":        "string",
				"description": "The branch name to push (e.g., 'agent/issue-123')",
			},
			"force": map[string]any{
				"type":        "boolean",
				"description": "Whether to force push",
				"default":     false,
			},
		},
		"required": []string{"branch"},
	}
}

func (t *PushBranch) Execute(ctx context.Context, args map[string]any) (any, error) {
	branch, ok := args["branch"].(string)
	if !ok || branch == "" {
		return nil, fmt.Errorf("branch is required")
	}
	if err := gitutil.ValidateBranchName(branch); err != nil {
		return nil, fmt.Errorf("invalid branch name: %w", err)
	}

	force, _ := args["force"].(bool)
	projectRoot := ProjectRootFromContext(ctx)
	if projectRoot == "" {
		projectRoot = t.ProjectRoot
	}

	t.Logger.Info("push_branch: starting push workflow", "branch", branch, "force", force, "dir", projectRoot)

	if err := t.ensureBranch(ctx, projectRoot, branch); err != nil {
		return nil, err
	}
	if err := t.commitPendingChanges(ctx, projectRoot); err != nil {
		return nil, err
	}
	output, err := t.pushToOrigin(ctx, projectRoot, branch, force)
	if err != nil {
		return nil, err
	}

	t.Logger.Info("push_branch: successfully pushed branch", "branch", branch)
	return map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Successfully pushed branch %s to origin", branch),
		"output":  output,
		"branch":  branch,
	}, nil
}

// ensureBranch checks out or creates the target branch if not already on it.
func (t *PushBranch) ensureBranch(ctx context.Context, projectRoot, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	current := strings.TrimSpace(string(out))
	if current == branch {
		return nil
	}

	t.Logger.Info("push_branch: switching to branch", "from", current, "to", branch)

	// Try existing branch first, then create
	checkout := exec.CommandContext(ctx, "git", "checkout", branch)
	checkout.Dir = projectRoot
	if _, err := checkout.CombinedOutput(); err != nil {
		create := exec.CommandContext(ctx, "git", "checkout", "-b", branch)
		create.Dir = projectRoot
		if out, err := create.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create branch %q: %w (output: %s)", branch, err, string(out))
		}
	}
	return nil
}

// commitPendingChanges stages and commits any uncommitted changes.
func (t *PushBranch) commitPendingChanges(ctx context.Context, projectRoot string) error {
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = projectRoot
	out, err := statusCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil // nothing to commit
	}

	t.Logger.Info("push_branch: committing uncommitted changes")

	addCmd := exec.CommandContext(ctx, "git", "add", ".")
	addCmd.Dir = projectRoot
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add changes: %w (output: %s)", err, string(out))
	}

	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", "Automated commit from code-warden agent")
	commitCmd.Dir = projectRoot
	if out, err := commitCmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "nothing to commit") {
			return fmt.Errorf("failed to commit changes: %w (output: %s)", err, string(out))
		}
	}
	return nil
}

// pushToOrigin pushes the branch to the remote origin.
func (t *PushBranch) pushToOrigin(ctx context.Context, projectRoot, branch string, force bool) (string, error) {
	args := []string{"push", "-u", "origin", branch}
	if force {
		args = append(args, "--force")
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to push branch %q: %w (output: %s)", branch, err, string(out))
	}
	return string(out), nil
}
