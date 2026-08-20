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
// It is constructed from raw GitHub webhook payloads and serves as the primary
// data carrier for triggering code review jobs.
type GitHubEvent struct {
	// Repository details
	RepoOwner    string // The repository owner's login name
	RepoName     string // The repository name
	RepoFullName string // The full name in "owner/repo" format
	RepoCloneURL string // The URL used to clone the repository
	Language     string // The primary programming language of the repository

	PRNumber int    // The pull request number
	PRTitle  string // The title of the pull request
	PRBody   string // The body/description of the pull request
	HeadSHA  string // The HEAD commit SHA of the PR

	// UserInstructions captures optional text provided with the command
	// (e.g., "/review check security"). This allows users to provide
	// custom guidance to the code review process.
	UserInstructions string

	// CommitMessages holds the commit messages for the PR, fetched from GitHub.
	CommitMessages []string

	Commenter      string // The GitHub username that triggered the review
	InstallationID int64  // The GitHub App installation ID

}

// EventFromIssueComment transforms a raw GitHub IssueCommentEvent into the application's
// internal GitHubEvent representation. It acts as an anti-corruption layer, validating
// the incoming webhook payload and extracting all necessary data before it's processed
// by a job. It specifically filters for comments that are "/review" or "/rereview"
// commands on pull requests.
//
// Returns an error if the comment is not on a pull request, the command is invalid,
// or required information is missing from the event.
func EventFromIssueComment(event *github.IssueCommentEvent) (*GitHubEvent, error) {
	if !event.GetIssue().IsPullRequest() {
		return nil, fmt.Errorf("comment is not on a pull request")
	}

	commentBody := strings.TrimSpace(strings.ToLower(event.GetComment().GetBody()))
	instructions, err := parseReviewCommand(commentBody)
	if err != nil {
		return nil, err
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
		RepoOwner:        repo.GetOwner().GetLogin(),
		RepoName:         repo.GetName(),
		RepoFullName:     repo.GetFullName(),
		RepoCloneURL:     repo.GetCloneURL(),
		Language:         repo.GetLanguage(),
		InstallationID:   event.GetInstallation().GetID(),
		PRNumber:         prNumber,
		PRTitle:          event.GetIssue().GetTitle(),
		PRBody:           event.GetIssue().GetBody(),
		UserInstructions: instructions,
		Commenter:        event.GetComment().GetUser().GetLogin(),
	}, nil
}

const reReviewCmd = "/rereview"

// sanitizeInstructions normalizes instructions by replacing whitespace characters
// with spaces and removing control characters. This prevents injection attacks
// and ensures consistent formatting.
func sanitizeInstructions(instructions string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 32 {
			return -1
		}
		return r
	}, instructions)
}

// parseReviewCommand parses the comment body and returns any user-provided
// instructions.
//
// Both "/review" and "/rereview" map to a full review: since the RAG re-review
// pipeline was removed, a re-review is identical to a fresh review of the
// current diff. The command is kept as an alias for backward compatibility.
//
// Returns the instructions string, and an error if the command is not recognized.
func parseReviewCommand(commentBody string) (string, error) {
	if commentBody == "/review" || commentBody == reReviewCmd {
		return "", nil
	}

	// Accept "/rereview <instructions>" (with a space).
	if strings.HasPrefix(commentBody, reReviewCmd+" ") {
		args := strings.TrimPrefix(commentBody, reReviewCmd)
		return sanitizeInstructions(strings.TrimSpace(args)), nil
	}

	// Accept "/review <instructions>" (with a space).
	if strings.HasPrefix(commentBody, "/review ") {
		args := strings.TrimPrefix(commentBody, "/review")
		return sanitizeInstructions(strings.TrimSpace(args)), nil
	}

	return "", fmt.Errorf("comment is not a valid review command: expected /review or /rereview")
}
