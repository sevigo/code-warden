// Package github provides functionality for interacting with the GitHub API.
package github

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v73/github"

	"github.com/sevigo/code-warden/internal/core"
)

// StatusUpdater defines the contract for updating the status of a GitHub Check Run
// and posting comments on pull requests.
type StatusUpdater interface {
	InProgress(ctx context.Context, event *core.GitHubEvent, title, summary string) (int64, error)
	Completed(ctx context.Context, event *core.GitHubEvent, checkRunID int64, conclusion, title, summary string) error
	PostReviewComment(ctx context.Context, event *core.GitHubEvent, body string) error
}

type statusUpdater struct {
	client GitHubClient
}

// NewStatusUpdater creates and returns a new instance of a statusUpdater.
func NewStatusUpdater(client GitHubClient) StatusUpdater {
	return &statusUpdater{client: client}
}

// InProgress creates a new GitHub Check Run with an "in_progress" status.
func (s *statusUpdater) InProgress(ctx context.Context, event *core.GitHubEvent, title, summary string) (int64, error) {
	opts := github.CreateCheckRunOptions{
		Name:    "Code-Warden Review",
		HeadSHA: event.HeadSHA,
		Status:  github.Ptr("in_progress"),
		Output: &github.CheckRunOutput{
			Title:   &title,
			Summary: &summary,
		},
	}
	checkRun, err := s.client.CreateCheckRun(ctx, event.RepoOwner, event.RepoName, opts)
	if err != nil {
		return 0, fmt.Errorf("failed to create check run: %w", err)
	}
	return checkRun.GetID(), nil
}

// Completed updates an existing GitHub Check Run to a "completed" status.
func (s *statusUpdater) Completed(ctx context.Context, event *core.GitHubEvent, checkRunID int64, conclusion, title, summary string) error {
	now := time.Now()
	opts := github.UpdateCheckRunOptions{
		Status:      github.Ptr("completed"),
		Conclusion:  &conclusion,
		CompletedAt: &github.Timestamp{Time: now},
		Output: &github.CheckRunOutput{
			Title:   &title,
			Summary: &summary,
		},
	}
	_, err := s.client.UpdateCheckRun(ctx, event.RepoOwner, event.RepoName, checkRunID, opts)
	return err
}

// PostReviewComment posts a new comment on the pull request or issue.
func (s *statusUpdater) PostReviewComment(ctx context.Context, event *core.GitHubEvent, body string) error {
	return s.client.CreateComment(ctx, event.RepoOwner, event.RepoName, event.PRNumber, body)
}
