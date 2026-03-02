package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/code-warden/internal/github"
)

// ListIssues lists issues in the repository.
type ListIssues struct {
	GHClient github.Client
	Repo     RepoIdentifier
	Logger   *slog.Logger
}

// ListIssuesResponse is the response for list_issues tool.
type ListIssuesResponse struct {
	Count  int            `json:"count"`
	Issues []github.Issue `json:"issues"`
}

func (t *ListIssues) Name() string {
	return "list_issues"
}

func (t *ListIssues) Description() string {
	return `List issues in the repository.
Returns a list of issues with their numbers, titles, and status.
Use this to find issues to work on or check issue status.`
}

func (t *ListIssues) InputSchema() map[string]any {
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

func (t *ListIssues) Execute(ctx context.Context, args map[string]any) (any, error) {
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

	t.Logger.Info("list_issues: listing issues",
		"owner", t.Repo.Owner,
		"repo", t.Repo.Name,
		"state", opts.State,
		"limit", opts.Limit)

	issues, err := t.GHClient.ListIssues(ctx, t.Repo.Owner, t.Repo.Name, opts)
	if err != nil {
		t.Logger.Error("list_issues: failed", "error", err)
		return nil, fmt.Errorf("failed to list issues: %w", err)
	}

	return ListIssuesResponse{
		Count:  len(issues),
		Issues: issues,
	}, nil
}

// GetIssue retrieves details of a specific issue.
type GetIssue struct {
	GHClient github.Client
	Repo     RepoIdentifier
	Logger   *slog.Logger
}

func (t *GetIssue) Name() string {
	return "get_issue"
}

func (t *GetIssue) Description() string {
	return `Get details of a specific issue by its number.
Returns the issue title, body, labels, and assignees.
Use this to understand what needs to be implemented for an issue.`
}

func (t *GetIssue) InputSchema() map[string]any {
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

func (t *GetIssue) Execute(ctx context.Context, args map[string]any) (any, error) {
	number, ok := args["number"].(float64)
	if !ok {
		return nil, fmt.Errorf("issue number is required")
	}
	issueNum := int(number)

	t.Logger.Info("get_issue: fetching issue",
		"owner", t.Repo.Owner,
		"repo", t.Repo.Name,
		"issue", issueNum)

	issue, err := t.GHClient.GetIssue(ctx, t.Repo.Owner, t.Repo.Name, issueNum)
	if err != nil {
		t.Logger.Error("get_issue: failed", "error", err)
		return nil, fmt.Errorf("failed to get issue #%d: %w", issueNum, err)
	}

	return issue, nil
}
