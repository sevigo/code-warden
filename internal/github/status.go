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
// and posting comments on pull requests. This interface abstracts the details of
// interacting with the GitHub Checks API and Issues API, providing a clean way
// to report the progress and results of automated code reviews.
type StatusUpdater interface {
	InProgress(ctx context.Context, event *core.GitHubEvent, title, summary string) (int64, error)
	Completed(ctx context.Context, event *core.GitHubEvent, checkRunID int64, conclusion, title, summary string) error
	PostReviewComment(ctx context.Context, event *core.GitHubEvent, body string) error
}

type statusUpdater struct {
	client GitHubClient
}

// NewStatusUpdater creates and returns a new instance of a statusUpdater.
// It takes a GitHubClient as a dependency, which should already be authenticated
// for the specific GitHub App installation.
//
// Parameters:
//
//	client: An authenticated GitHubClient instance.
//
// Returns:
//
//	StatusUpdater: A new instance of the status updater.
func NewStatusUpdater(client GitHubClient) StatusUpdater {
	return &statusUpdater{client: client}
}

// InProgress creates a new GitHub Check Run with an "in_progress" status.
// This function is called at the beginning of a review job to inform users
// that the automated review process has commenced. It returns the ID of the
// created check run, which is essential for updating its status later.
//
// Parameters:
//
//	ctx: The context for the API call.
//	event: The GitHubEvent containing details about the repository and pull request.
//	title: The title to display for the check run (e.g., "Code Review").
//	summary: A brief summary message for the check run (e.g., "AI analysis in progress...").
//
// Returns:
//
//	int64: The ID of the newly created GitHub Check Run.
//	error: An error if the API call to create the check run fails.
func (s *statusUpdater) InProgress(ctx context.Context, event *core.GitHubEvent, title, summary string) (int64, error) {
	opts := github.CreateCheckRunOptions{
		Name:    "Code-Warden Review",         // The name of the check run, visible in GitHub UI.
		HeadSHA: event.HeadSHA,                // The commit SHA against which the check run is performed.
		Status:  github.String("in_progress"), // Set the status to indicate ongoing work.
		Output: &github.CheckRunOutput{
			Title:   &title,   // The title displayed in the check run details.
			Summary: &summary, // A short description of the current state.
		},
	}
	checkRun, err := s.client.CreateCheckRun(ctx, event.RepoOwner, event.RepoName, opts)
	if err != nil {
		return 0, fmt.Errorf("failed to create check run: %w", err)
	}
	return checkRun.GetID(), nil
}

// Completed updates an existing GitHub Check Run to a "completed" status.
// This function is called at the end of a review job to report the final outcome.
// It sets the conclusion (e.g., "success", "failure", "neutral") and provides
// a final summary message.
//
// Parameters:
//
//	ctx: The context for the API call.
//	event: The GitHubEvent containing repository details.
//	checkRunID: The ID of the check run to update, obtained from the InProgress call.
//	conclusion: The final conclusion of the check run (e.g., "success", "failure", "neutral").
//	title: The final title for the check run.
//	summary: The final summary message for the check run.
//
// Returns:
//
//	error: An error if the API call to update the check run fails.
func (s *statusUpdater) Completed(ctx context.Context, event *core.GitHubEvent, checkRunID int64, conclusion, title, summary string) error {
	now := time.Now()
	opts := github.UpdateCheckRunOptions{
		Status:      github.String("completed"),   // Mark the check run as completed.
		Conclusion:  &conclusion,                  // Set the final outcome.
		CompletedAt: &github.Timestamp{Time: now}, // Record the completion timestamp.
		Output: &github.CheckRunOutput{
			Title:   &title,
			Summary: &summary,
		},
	}
	_, err := s.client.UpdateCheckRun(ctx, event.RepoOwner, event.RepoName, checkRunID, opts)
	return err
}

// PostReviewComment posts a new comment on the pull request or issue.
// This function is used to publish the AI-generated code review directly
// to the GitHub pull request, making it visible to developers.
//
// Parameters:
//
//	ctx: The context for the API call.
//	event: The GitHubEvent containing the pull request number and repository details.
//	body: The content of the comment to post.
//
// Returns:
//
//	error: An error if the API call to create the comment fails.
func (s *statusUpdater) PostReviewComment(ctx context.Context, event *core.GitHubEvent, body string) error {
	return s.client.CreateComment(ctx, event.RepoOwner, event.RepoName, event.PRNumber, body)
}
