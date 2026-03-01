// Package mcp provides GitHub-related MCP tools for agent operations.
// These tools allow agents to interact with GitHub issues and pull requests
// through the Model Context Protocol interface.
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"

	"github.com/sevigo/code-warden/internal/github"
)

// validBranchName matches safe Git branch names: alphanumeric, slashes, hyphens, underscores, dots.
// Rejects empty strings, double dots (..), and leading or trailing slashes.
var validBranchName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9/_\-\.]*[a-zA-Z0-9])?$`)

// validateBranchName checks that a branch name is safe to pass to exec.CommandContext.
func validateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	if len(name) > 255 {
		return fmt.Errorf("branch name too long (max 255 chars)")
	}
	if !validBranchName.MatchString(name) {
		return fmt.Errorf("branch name %q contains invalid characters", name)
	}
	return nil
}

// CreatePRTool creates a pull request in the repository.
// It requires the agent to have write access to the repository.
type CreatePRTool struct {
	ghClient github.Client
	repo     struct {
		owner string
		name  string
	}
	logger *slog.Logger
}

// NewCreatePRTool creates a new CreatePRTool.
func NewCreatePRTool(ghClient github.Client, owner, repo string, logger *slog.Logger) *CreatePRTool {
	return &CreatePRTool{
		ghClient: ghClient,
		repo: struct {
			owner string
			name  string
		}{owner: owner, name: repo},
		logger: logger,
	}
}

func (t *CreatePRTool) Name() string {
	return "create_pull_request"
}

func (t *CreatePRTool) Description() string {
	return `Create a pull request in the repository.
Returns the PR number and URL.
Use this after making changes and running tests/reviews.`
}

func (t *CreatePRTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{
				"type":        "string",
				"description": "The title of the pull request",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "The body/description of the pull request",
			},
			"head": map[string]any{
				"type":        "string",
				"description": "The branch containing changes (e.g., 'feature/my-feature')",
			},
			"base": map[string]any{
				"type":        "string",
				"description": "The branch to merge into (default: 'main')",
				"default":     "main",
			},
			"draft": map[string]any{
				"type":        "boolean",
				"description": "Whether to create as a draft PR",
				"default":     false,
			},
		},
		"required": []string{"title", "body", "head"},
	}
}

func (t *CreatePRTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	title, ok := args["title"].(string)
	if !ok || title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if len(title) > maxTitleLength {
		return nil, fmt.Errorf("title exceeds maximum length of %d characters", maxTitleLength)
	}

	body, ok := args["body"].(string)
	if !ok || body == "" {
		return nil, fmt.Errorf("body is required")
	}
	if len(body) > maxBodyLength {
		return nil, fmt.Errorf("body exceeds maximum length of %d characters", maxBodyLength)
	}

	head, ok := args["head"].(string)
	if !ok || head == "" {
		return nil, fmt.Errorf("head branch is required")
	}

	base := "main"
	if b, ok := args["base"].(string); ok && b != "" {
		base = b
	}

	// Validate base branch exists
	if _, err := t.ghClient.GetBranch(ctx, t.repo.owner, t.repo.name, base); err != nil {
		return nil, fmt.Errorf("base branch %q does not exist: %w", base, err)
	}

	opts := github.PullRequestOptions{
		Title: title,
		Body:  body,
		Head:  head,
		Base:  base,
	}

	if draft, ok := args["draft"].(bool); ok {
		opts.Draft = draft
	}

	t.logger.Info("create_pull_request: creating PR",
		"owner", t.repo.owner,
		"repo", t.repo.name,
		"head", head,
		"base", opts.Base)

	pr, err := t.ghClient.CreatePullRequest(ctx, t.repo.owner, t.repo.name, opts)
	if err != nil {
		t.logger.Error("create_pull_request: failed", "error", err)
		return nil, fmt.Errorf("failed to create pull request: %w", err)
	}

	return CreatePRResponse{
		Number: pr.GetNumber(),
		URL:    pr.GetHTMLURL(),
		State:  pr.GetState(),
	}, nil
}

// CreatePRResponse is the response for create_pull_request tool.
type CreatePRResponse struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// ListIssuesTool lists issues in the repository.
type ListIssuesTool struct {
	ghClient github.Client
	repo     struct {
		owner string
		name  string
	}
	logger *slog.Logger
}

// NewListIssuesTool creates a new ListIssuesTool.
func NewListIssuesTool(ghClient github.Client, owner, repo string, logger *slog.Logger) *ListIssuesTool {
	return &ListIssuesTool{
		ghClient: ghClient,
		repo: struct {
			owner string
			name  string
		}{owner: owner, name: repo},
		logger: logger,
	}
}

func (t *ListIssuesTool) Name() string {
	return "list_issues"
}

func (t *ListIssuesTool) Description() string {
	return `List issues in the repository.
Returns a list of issues with their numbers, titles, and status.
Use this to find issues to work on or check issue status.`
}

func (t *ListIssuesTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"state": map[string]any{
				"type":        "string",
				"description": "Filter by state: 'open', 'closed', 'all'",
				"default":     "open",
				"enum":        []string{"open", "closed", "all"},
			},
			"labels": map[string]any{
				"type":        "array",
				"description": "Filter by labels (e.g., ['bug', 'enhancement'])",
				"items": map[string]any{
					"type": "string",
				},
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of issues to return (default: 30, max: 100)",
				"default":     30,
			},
		},
	}
}

func (t *ListIssuesTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	opts := github.IssueOptions{
		State: "open",
		Limit: 30,
	}

	if state, ok := args["state"].(string); ok && state != "" {
		opts.State = state
	}

	if limit, ok := args["limit"].(float64); ok {
		opts.Limit = int(limit)
	}

	if labels, ok := args["labels"].([]any); ok {
		for _, label := range labels {
			if labelStr, ok := label.(string); ok {
				opts.Labels = append(opts.Labels, labelStr)
			}
		}
	}

	t.logger.Info("list_issues: listing issues",
		"owner", t.repo.owner,
		"repo", t.repo.name,
		"state", opts.State,
		"limit", opts.Limit)

	issues, err := t.ghClient.ListIssues(ctx, t.repo.owner, t.repo.name, opts)
	if err != nil {
		t.logger.Error("list_issues: failed", "error", err)
		return nil, fmt.Errorf("failed to list issues: %w", err)
	}

	return ListIssuesResponse{
		Count:  len(issues),
		Issues: issues,
	}, nil
}

// ListIssuesResponse is the response for list_issues tool.
type ListIssuesResponse struct {
	Count  int            `json:"count"`
	Issues []github.Issue `json:"issues"`
}

// GetIssueTool gets details of a specific issue.
type GetIssueTool struct {
	ghClient github.Client
	repo     struct {
		owner string
		name  string
	}
	logger *slog.Logger
}

// NewGetIssueTool creates a new GetIssueTool.
func NewGetIssueTool(ghClient github.Client, owner, repo string, logger *slog.Logger) *GetIssueTool {
	return &GetIssueTool{
		ghClient: ghClient,
		repo: struct {
			owner string
			name  string
		}{owner: owner, name: repo},
		logger: logger,
	}
}

func (t *GetIssueTool) Name() string {
	return "get_issue"
}

func (t *GetIssueTool) Description() string {
	return `Get details of a specific issue by its number.
Returns the issue title, body, labels, and assignees.
Use this to understand what needs to be implemented for an issue.`
}

func (t *GetIssueTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"number": map[string]any{
				"type":        "integer",
				"description": "The issue number",
			},
		},
		"required": []string{"number"},
	}
}

func (t *GetIssueTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	number, ok := args["number"].(float64)
	if !ok {
		return nil, fmt.Errorf("issue number is required")
	}
	issueNum := int(number)

	t.logger.Info("get_issue: fetching issue",
		"owner", t.repo.owner,
		"repo", t.repo.name,
		"issue", issueNum)

	issue, err := t.ghClient.GetIssue(ctx, t.repo.owner, t.repo.name, issueNum)
	if err != nil {
		t.logger.Error("get_issue: failed", "error", err)
		return nil, fmt.Errorf("failed to get issue #%d: %w", issueNum, err)
	}

	return issue, nil
}

// PushBranchTool pushes a local branch to the remote repository.
type PushBranchTool struct {
	logger      *slog.Logger
	projectRoot string
}

// NewPushBranchTool creates a new PushBranchTool.
func NewPushBranchTool(projectRoot string, logger *slog.Logger) *PushBranchTool {
	return &PushBranchTool{
		projectRoot: projectRoot,
		logger:      logger,
	}
}

func (t *PushBranchTool) Name() string {
	return "push_branch"
}

func (t *PushBranchTool) Description() string {
	return `Push current local changes to a remote branch.
You MUST call this before create_pull_request to ensure the remote branch exists.`
}

func (t *PushBranchTool) InputSchema() map[string]any {
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

func (t *PushBranchTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	branch, ok := args["branch"].(string)
	if !ok || branch == "" {
		return nil, fmt.Errorf("branch is required")
	}
	if err := validateBranchName(branch); err != nil {
		return nil, fmt.Errorf("invalid branch name: %w", err)
	}

	force, _ := args["force"].(bool)
	t.logger.Info("push_branch: starting push workflow", "branch", branch, "force", force, "dir", t.projectRoot)

	if err := t.ensureBranch(ctx, branch); err != nil {
		return nil, err
	}
	if err := t.commitPendingChanges(ctx); err != nil {
		return nil, err
	}
	output, err := t.pushToOrigin(ctx, branch, force)
	if err != nil {
		return nil, err
	}

	t.logger.Info("push_branch: successfully pushed branch", "branch", branch)
	return map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Successfully pushed branch %s to origin", branch),
		"output":  output,
		"branch":  branch,
	}, nil
}

// ensureBranch checks out or creates the target branch if not already on it.
func (t *PushBranchTool) ensureBranch(ctx context.Context, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = t.projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	current := strings.TrimSpace(string(out))
	if current == branch {
		return nil
	}

	t.logger.Info("push_branch: switching to branch", "from", current, "to", branch)

	// Try existing branch first, then create
	checkout := exec.CommandContext(ctx, "git", "checkout", branch)
	checkout.Dir = t.projectRoot
	if _, err := checkout.CombinedOutput(); err != nil {
		create := exec.CommandContext(ctx, "git", "checkout", "-b", branch)
		create.Dir = t.projectRoot
		if out, err := create.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create branch '%s': %w (output: %s)", branch, err, string(out))
		}
	}
	return nil
}

// commitPendingChanges stages and commits any uncommitted changes.
func (t *PushBranchTool) commitPendingChanges(ctx context.Context) error {
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = t.projectRoot
	out, err := statusCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil // nothing to commit
	}

	t.logger.Info("push_branch: committing uncommitted changes")

	addCmd := exec.CommandContext(ctx, "git", "add", "-A")
	addCmd.Dir = t.projectRoot
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add changes: %w (output: %s)", err, string(out))
	}

	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", "Automated commit from code-warden agent")
	commitCmd.Dir = t.projectRoot
	if out, err := commitCmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "nothing to commit") {
			return fmt.Errorf("failed to commit changes: %w (output: %s)", err, string(out))
		}
	}
	return nil
}

// pushToOrigin pushes the branch to the remote origin.
func (t *PushBranchTool) pushToOrigin(ctx context.Context, branch string, force bool) (string, error) {
	args := []string{"push", "-u", "origin", branch}
	if force {
		args = append(args, "--force")
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = t.projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to push branch '%s': %w (output: %s)", branch, err, string(out))
	}
	return string(out), nil
}
