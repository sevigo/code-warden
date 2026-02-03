// Package github provides functionality for interacting with the GitHub API.
package github

import (
	"context"
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
	Path string
	Line int
	Body string
}

// Client defines a set of operations for interacting with the GitHub API,
// focusing on pull requests, comments, and check runs.
//
//go:generate mockgen -destination=../../mocks/mock_github_client.go -package=mocks . Client
type Client interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error)
	GetPullRequestDiff(ctx context.Context, owner, repo string, number int) (string, error)
	GetChangedFiles(ctx context.Context, owner, repo string, number int) ([]ChangedFile, error)
	CreateComment(ctx context.Context, owner, repo string, number int, body string) error
	CreateReview(ctx context.Context, owner, repo string, number int, body string, comments []DraftReviewComment) error
	CreateCheckRun(ctx context.Context, owner, repo string, opts github.CreateCheckRunOptions) (*github.CheckRun, error)
	UpdateCheckRun(ctx context.Context, owner, repo string, checkRunID int64, opts github.UpdateCheckRunOptions) (*github.CheckRun, error)
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

// CreateReview creates a new pull request review with a summary and line-specific comments.
func (g *gitHubClient) CreateReview(ctx context.Context, owner, repo string, number int, body string, comments []DraftReviewComment) error {
	var ghComments []*github.DraftReviewComment
	for _, c := range comments {
		ghComments = append(ghComments, &github.DraftReviewComment{
			Path: &c.Path,
			Line: &c.Line,
			Body: &c.Body,
		})
	}

	reviewRequest := &github.PullRequestReviewRequest{
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
