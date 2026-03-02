package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
)

// CreatePullRequest creates a pull request in the repository.
type CreatePullRequest struct {
	GHClient github.Client
	Repo     RepoIdentifier
	Logger   *slog.Logger
}

// RepoIdentifier holds owner and name for a repository.
type RepoIdentifier struct {
	Owner string
	Name  string
}

// CreatePRResponse is the response for create_pull_request tool.
type CreatePRResponse struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

func (t *CreatePullRequest) Name() string {
	return "create_pull_request"
}

func (t *CreatePullRequest) Description() string {
	return `Create a pull request in the repository.
Returns the PR number and URL.
Use this after making changes and running tests/reviews.`
}

func (t *CreatePullRequest) InputSchema() map[string]any {
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

func (t *CreatePullRequest) Execute(ctx context.Context, args map[string]any) (any, error) {
	title, ok := args["title"].(string)
	if !ok || title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if len(title) > MaxTitleLength {
		return nil, fmt.Errorf("title exceeds maximum length of %d characters", MaxTitleLength)
	}

	body, ok := args["body"].(string)
	if !ok || body == "" {
		return nil, fmt.Errorf("body is required")
	}
	if len(body) > MaxBodyLength {
		return nil, fmt.Errorf("body exceeds maximum length of %d characters", MaxBodyLength)
	}

	head, ok := args["head"].(string)
	if !ok || head == "" {
		return nil, fmt.Errorf("head branch is required")
	}
	if err := gitutil.ValidateBranchName(head); err != nil {
		return nil, fmt.Errorf("invalid head branch: %w", err)
	}

	base := "main"
	if b, ok := args["base"].(string); ok && b != "" {
		base = b
	}

	// Validate base branch exists
	if _, err := t.GHClient.GetBranch(ctx, t.Repo.Owner, t.Repo.Name, base); err != nil {
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

	// Validate head branch exists on remote
	if _, err := t.GHClient.GetBranch(ctx, t.Repo.Owner, t.Repo.Name, head); err != nil {
		return nil, fmt.Errorf("head branch %q does not exist on remote: %w. You MUST call push_branch before create_pull_request", head, err)
	}

	t.Logger.Info("create_pull_request: creating PR",
		"owner", t.Repo.Owner,
		"repo", t.Repo.Name,
		"head", head,
		"base", opts.Base)

	pr, err := t.GHClient.CreatePullRequest(ctx, t.Repo.Owner, t.Repo.Name, opts)
	if err != nil {
		t.Logger.Error("create_pull_request: failed", "error", err)
		return nil, fmt.Errorf("failed to create pull request: %w", err)
	}

	return CreatePRResponse{
		Number: pr.GetNumber(),
		URL:    pr.GetHTMLURL(),
		State:  pr.GetState(),
	}, nil
}
