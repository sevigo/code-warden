// Package mcp provides GitHub-related MCP tools for agent operations.
// These tools allow agents to interact with GitHub issues and pull requests
// through the Model Context Protocol interface.
package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/code-warden/internal/github"
)

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

	head, ok := args["head"].(string)
	if !ok || head == "" {
		return nil, fmt.Errorf("head branch is required")
	}

	opts := github.PullRequestOptions{
		Title: title,
		Body:  body,
		Head:  head,
	}

	if base, ok := args["base"].(string); ok && base != "" {
		opts.Base = base
	} else {
		opts.Base = "main"
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
