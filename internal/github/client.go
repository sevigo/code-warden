// Package github provides functionality for interacting with the GitHub API.
package github

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/go-github/v73/github"
	"golang.org/x/oauth2"
)

// ChangedFile holds the filename and patch data for a single file
// included in a pull request. This helps in focusing the review on specific changes.
type ChangedFile struct {
	Filename string
	Patch    string
}

// DraftReviewComment represents a single comment to be posted as part of a review.
type DraftReviewComment struct {
	Path      string
	Line      int
	StartLine int // Optional, for multi-line comments
	Body      string
}

// PullRequestOptions contains options for creating a pull request.
type PullRequestOptions struct {
	Title string
	Body  string
	Head  string // Branch with changes
	Base  string // Branch to merge into (default: "main")
	Draft bool
}

// IssueOptions contains options for listing issues.
type IssueOptions struct {
	State    string // "open", "closed", "all" (default: "open")
	Labels   []string
	Assignee string
	Limit    int // Max issues to return (default: 30)
}

// Issue represents a GitHub issue.
type Issue struct {
	Number    int
	Title     string
	Body      string
	State     string
	Labels    []string
	Assignees []string
	URL       string
}

// Client defines a set of operations for interacting with the GitHub API,
// focusing on pull requests, comments, and check runs.
//
//go:generate mockgen -destination=../../mocks/mock_github_client.go -package=mocks . Client
type Client interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error)
	GetPullRequestDiff(ctx context.Context, owner, repo string, number int) (string, error)
	GetPullRequestCommits(ctx context.Context, owner, repo string, number int) ([]string, error)
	GetChangedFiles(ctx context.Context, owner, repo string, number int) ([]ChangedFile, error)
	CreateComment(ctx context.Context, owner, repo string, number int, body string) error
	CreateReview(ctx context.Context, owner, repo string, number int, commitSHA, body string, comments []DraftReviewComment) error
	CreateCheckRun(ctx context.Context, owner, repo string, opts github.CreateCheckRunOptions) (*github.CheckRun, error)
	UpdateCheckRun(ctx context.Context, owner, repo string, checkRunID int64, opts github.UpdateCheckRunOptions) (*github.CheckRun, error)

	// New methods for agent operations
	CreatePullRequest(ctx context.Context, owner, repo string, opts PullRequestOptions) (*github.PullRequest, error)
	ListIssues(ctx context.Context, owner, repo string, opts IssueOptions) ([]Issue, error)
	GetIssue(ctx context.Context, owner, repo string, number int) (*Issue, error)
	GetBranch(ctx context.Context, owner, repo, branch string) (*github.Branch, error)
}

type gitHubClient struct {
	client *github.Client
	logger *slog.Logger
}

// NewGitHubClient wraps the official go-github client to provide a focused,
// testable interface for application-specific GitHub operations.
func NewGitHubClient(client *github.Client, logger *slog.Logger) Client {
	return &gitHubClient{client: client, logger: logger}
}

// NewPATClient creates a new GitHub client authenticated with a Personal Access Token (PAT).
// This is useful for CLI tools or local development where an App installation is not available.
func NewPATClient(ctx context.Context, token string, logger *slog.Logger) Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)
	return &gitHubClient{client: client, logger: logger}
}

const diffSideRight = "RIGHT"

// CreateReview creates a new pull request review with a summary and line-specific comments.
func (g *gitHubClient) CreateReview(ctx context.Context, owner, repo string, number int, commitSHA, body string, comments []DraftReviewComment) error {
	var ghComments []*github.DraftReviewComment
	for _, c := range comments {
		comment := &github.DraftReviewComment{
			Path: &c.Path,
			Line: &c.Line,
			Body: &c.Body,
		}

		if c.StartLine > 0 && c.StartLine != c.Line {
			comment.StartLine = &c.StartLine
			// StartSide must be provided for multi-line comments per GitHub API spec
			// Side is also required if StartLine is provided.
			side := diffSideRight
			comment.StartSide = &side
			comment.Side = &side
		} else {
			// Explicitly set Side to RIGHT for single line comments as well
			side := diffSideRight
			comment.Side = &side
		}
		ghComments = append(ghComments, comment)
	}

	reviewRequest := &github.PullRequestReviewRequest{
		CommitID: &commitSHA,
		Body:     &body,
		Event:    github.Ptr("COMMENT"),
		Comments: ghComments,
	}

	_, _, err := g.client.PullRequests.CreateReview(ctx, owner, repo, number, reviewRequest)
	if err != nil {
		g.logger.Error("failed to create pull request review", "owner", owner, "repo", repo, "pr", number, "error", err)
	}
	return err
}

// GetPullRequest retrieves a single pull request by its number.
func (g *gitHubClient) GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	pr, _, err := g.client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		g.logger.Error("failed to get pull request", "owner", owner, "repo", repo, "pr", number, "error", err)
		return nil, err
	}
	return pr, nil
}

// GetPullRequestDiff retrieves the diff of a pull request as a string.
func (g *gitHubClient) GetPullRequestDiff(ctx context.Context, owner, repo string, number int) (string, error) {
	diff, _, err := g.client.PullRequests.GetRaw(ctx, owner, repo, number, github.RawOptions{
		Type: github.Diff,
	})
	if err != nil {
		g.logger.Error("failed to get pull request diff", "owner", owner, "repo", repo, "pr", number, "error", err)
		return "", err
	}
	return diff, nil
}

// GetPullRequestCommits retrieves commit messages for a pull request (up to 50 commits).
func (g *gitHubClient) GetPullRequestCommits(ctx context.Context, owner, repo string, number int) ([]string, error) {
	commits, _, err := g.client.PullRequests.ListCommits(ctx, owner, repo, number, &github.ListOptions{PerPage: 50})
	if err != nil {
		g.logger.Warn("failed to list commits for pull request", "owner", owner, "repo", repo, "pr", number, "error", err)
		return nil, err
	}
	messages := make([]string, 0, len(commits))
	for _, c := range commits {
		if msg := c.GetCommit().GetMessage(); msg != "" {
			messages = append(messages, msg)
		}
	}
	return messages, nil
}

// GetChangedFiles retrieves the list of files modified in a pull request.
// It handles pagination automatically to ensure all files are fetched
// from the GitHub API, which returns a maximum of 100 files per page.
func (g *gitHubClient) GetChangedFiles(ctx context.Context, owner, repo string, number int) ([]ChangedFile, error) {
	var allFiles []ChangedFile
	opts := &github.ListOptions{PerPage: 100}

	for {
		files, resp, err := g.client.PullRequests.ListFiles(ctx, owner, repo, number, opts)
		if err != nil {
			g.logger.Error("failed to list files for pull request", "owner", owner, "repo", repo, "pr", number, "error", err)
			return nil, err
		}

		for _, file := range files {
			patch := ""
			if file.Patch != nil {
				patch = *file.Patch
			}
			allFiles = append(allFiles, ChangedFile{
				Filename: *file.Filename,
				Patch:    patch,
			})
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allFiles, nil
}

// CreateComment creates a new comment on a pull request.
func (g *gitHubClient) CreateComment(ctx context.Context, owner, repo string, number int, body string) error {
	comment := &github.IssueComment{Body: &body}
	_, _, err := g.client.Issues.CreateComment(ctx, owner, repo, number, comment)
	if err != nil {
		g.logger.Error("failed to create comment", "owner", owner, "repo", repo, "pr", number, "error", err)
	}
	return err
}

// CreateCheckRun creates a new check run.
func (g *gitHubClient) CreateCheckRun(ctx context.Context, owner, repo string, opts github.CreateCheckRunOptions) (*github.CheckRun, error) {
	checkRun, _, err := g.client.Checks.CreateCheckRun(ctx, owner, repo, opts)
	if err != nil {
		g.logger.Error("failed to create check run", "owner", owner, "repo", repo, "error", err)
		return nil, err
	}
	return checkRun, nil
}

// UpdateCheckRun updates an existing check run.
func (g *gitHubClient) UpdateCheckRun(ctx context.Context, owner, repo string, checkRunID int64, opts github.UpdateCheckRunOptions) (*github.CheckRun, error) {
	checkRun, _, err := g.client.Checks.UpdateCheckRun(ctx, owner, repo, checkRunID, opts)
	if err != nil {
		g.logger.Error("failed to update check run", "owner", owner, "repo", repo, "checkRunID", checkRunID, "error", err)
	}
	return checkRun, err
}

// CreatePullRequest creates a new pull request.
func (g *gitHubClient) CreatePullRequest(ctx context.Context, owner, repo string, opts PullRequestOptions) (*github.PullRequest, error) {
	if opts.Base == "" {
		opts.Base = "main"
	}

	prOpts := &github.NewPullRequest{
		Title: &opts.Title,
		Body:  &opts.Body,
		Head:  &opts.Head,
		Base:  &opts.Base,
		Draft: &opts.Draft,
	}

	pr, _, err := g.client.PullRequests.Create(ctx, owner, repo, prOpts)
	if err != nil {
		g.logger.Error("failed to create pull request", "owner", owner, "repo", repo, "head", opts.Head, "error", err)
		return nil, err
	}

	g.logger.Info("created pull request", "owner", owner, "repo", repo, "pr", pr.GetNumber(), "url", pr.GetHTMLURL())
	return pr, nil
}

// ListIssues lists issues in a repository.
func (g *gitHubClient) ListIssues(ctx context.Context, owner, repo string, opts IssueOptions) ([]Issue, error) {
	if opts.State == "" {
		opts.State = "open"
	}
	if opts.Limit == 0 {
		opts.Limit = 30
	}
	if opts.Limit > 100 {
		opts.Limit = 100
	}

	issueOpts := &github.IssueListByRepoOptions{
		State: opts.State,
		ListOptions: github.ListOptions{
			PerPage: opts.Limit,
		},
	}

	if len(opts.Labels) > 0 {
		issueOpts.Labels = opts.Labels
	}
	if opts.Assignee != "" {
		issueOpts.Assignee = opts.Assignee
	}

	issues, _, err := g.client.Issues.ListByRepo(ctx, owner, repo, issueOpts)
	if err != nil {
		g.logger.Error("failed to list issues", "owner", owner, "repo", repo, "error", err)
		return nil, err
	}

	result := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.PullRequestLinks != nil {
			// Skip pull requests (they appear in issues list too)
			continue
		}

		labels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			labels = append(labels, label.GetName())
		}

		assignees := make([]string, 0, len(issue.Assignees))
		for _, assignee := range issue.Assignees {
			assignees = append(assignees, assignee.GetLogin())
		}

		result = append(result, Issue{
			Number:    issue.GetNumber(),
			Title:     issue.GetTitle(),
			Body:      issue.GetBody(),
			State:     issue.GetState(),
			Labels:    labels,
			Assignees: assignees,
			URL:       issue.GetHTMLURL(),
		})
	}

	return result, nil
}

// GetIssue retrieves a single issue by its number.
func (g *gitHubClient) GetIssue(ctx context.Context, owner, repo string, number int) (*Issue, error) {
	issue, _, err := g.client.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		g.logger.Error("failed to get issue", "owner", owner, "repo", repo, "issue", number, "error", err)
		return nil, err
	}

	// Check if it's actually a pull request
	if issue.PullRequestLinks != nil {
		return nil, fmt.Errorf("issue #%d is a pull request, not an issue", number)
	}

	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label.GetName())
	}

	assignees := make([]string, 0, len(issue.Assignees))
	for _, assignee := range issue.Assignees {
		assignees = append(assignees, assignee.GetLogin())
	}

	return &Issue{
		Number:    issue.GetNumber(),
		Title:     issue.GetTitle(),
		Body:      issue.GetBody(),
		State:     issue.GetState(),
		Labels:    labels,
		Assignees: assignees,
		URL:       issue.GetHTMLURL(),
	}, nil
}

// GetBranch retrieves a single branch by its name.
func (g *gitHubClient) GetBranch(ctx context.Context, owner, repo, branch string) (*github.Branch, error) {
	b, _, err := g.client.Repositories.GetBranch(ctx, owner, repo, branch, 0)
	if err != nil {
		g.logger.Warn("failed to get branch", "owner", owner, "repo", repo, "branch", branch, "error", err)
		return nil, err
	}
	return b, nil
}
