// Package core defines the essential interfaces and data structures that form the
// backbone of the application. These components are designed to be abstract,
// allowing for flexible and decoupled implementations of the application's logic.
package core

import (
	"fmt"
	"strings"

	"github.com/google/go-github/v73/github"
)

// GitHubEvent represents a simplified, internal view of a GitHub webhook event.
type GitHubEvent struct {
	// Repository details
	RepoOwner    string
	RepoName     string
	RepoFullName string
	RepoCloneURL string
	Language     string

	PRNumber int
	PRTitle  string
	PRBody   string
	HeadSHA  string

	Commenter      string
	InstallationID int64
}

// EventFromIssueComment transforms a raw GitHub IssueCommentEvent into the application's
// internal GitHubEvent representation. It acts as an anti-corruption layer, ensuring
// that the incoming webhook payload is valid and contains all necessary data before
// it's processed by a job. It specifically filters for comments that are a "/review" command
// on a pull request.
func EventFromIssueComment(event *github.IssueCommentEvent) (*GitHubEvent, error) {
	if !event.GetIssue().IsPullRequest() {
		return nil, fmt.Errorf("comment is not on a pull request")
	}

	if !strings.EqualFold(strings.TrimSpace(event.GetComment().GetBody()), "/review") {
		return nil, fmt.Errorf("comment is not a review command")
	}

	repo := event.GetRepo()
	if repo == nil || repo.GetOwner() == nil || repo.GetOwner().GetLogin() == "" || repo.GetName() == "" {
		return nil, fmt.Errorf("repository or owner information is missing from the event")
	}

	prNumber := event.GetIssue().GetNumber()
	if prNumber <= 0 {
		return nil, fmt.Errorf("invalid pull request number: %d", prNumber)
	}

	if event.GetComment().GetUser() == nil || event.GetComment().GetUser().GetLogin() == "" {
		return nil, fmt.Errorf("commenter information is missing from the event")
	}

	if event.GetInstallation() == nil || event.GetInstallation().GetID() == 0 {
		return nil, fmt.Errorf("installation ID is missing from the event")
	}

	return &GitHubEvent{
		RepoOwner:      repo.GetOwner().GetLogin(),
		RepoName:       repo.GetName(),
		RepoFullName:   repo.GetFullName(),
		RepoCloneURL:   repo.GetCloneURL(),
		Language:       repo.GetLanguage(),
		InstallationID: event.GetInstallation().GetID(),
		PRNumber:       prNumber,
		PRTitle:        event.GetIssue().GetTitle(),
		PRBody:         event.GetIssue().GetBody(),
		Commenter:      event.GetComment().GetUser().GetLogin(),
	}, nil
}
