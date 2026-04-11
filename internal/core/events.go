// Package core defines the essential interfaces and data structures that form the
// backbone of the application. These components are designed to be abstract,
// allowing for flexible and decoupled implementations of the application's logic.
package core

import (
	"fmt"
	"strings"

	"github.com/google/go-github/v73/github"
)

// ReviewType distinguishes between a full review and a follow-up review.
type ReviewType int

const (
	// FullReview indicates a complete code review should be performed.
	FullReview ReviewType = iota
	// ReReview indicates a follow-up review of changes since a previous review.
	ReReview
	// ImplementIssue indicates an autonomous agent should implement the issue.
	ImplementIssue
	// Reindex indicates a background RAG index refresh triggered by a push
	// to the default branch. No review is performed — only the vector store
	// is updated to keep the index current.
	Reindex
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

	// Type specifies whether this is a FullReview or a ReReview request.
	Type ReviewType

	// UserInstructions captures optional text provided with the command
	// (e.g., "/rereview check security"). This allows users to provide
	// custom guidance to the code review process.
	UserInstructions string

	// CommitMessages holds the commit messages for the PR, fetched from GitHub.
	// Populated before review generation and included in the RAG context query.
	CommitMessages []string

	Commenter      string // The GitHub username that triggered the review
	InstallationID int64  // The GitHub App installation ID

	// Fields for ImplementIssue type
	IssueNumber int    // The issue number (for /implement commands)
	IssueTitle  string // The title of the issue
	IssueBody   string // The body/description of the issue
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
	reviewType, instructions, err := parseReviewCommand(commentBody)
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
		Type:             reviewType,
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

// parseReviewCommand parses the comment body to determine the review type
// and any user-provided instructions.
//
// Returns the ReviewType, instructions string, and an error if the command
// is not recognized.
func parseReviewCommand(commentBody string) (ReviewType, string, error) {
	if commentBody == "/review" {
		return FullReview, "", nil
	}

	if !strings.HasPrefix(commentBody, reReviewCmd) {
		return 0, "", fmt.Errorf("comment is not a valid review command: expected /review or /rereview")
	}

	// Ensure it's "/rereview" exactly or "/rereview " (with space)
	if commentBody != reReviewCmd && !strings.HasPrefix(commentBody, reReviewCmd+" ") {
		return 0, "", fmt.Errorf("comment is not a valid review command: expected /review or /rereview")
	}

	args := strings.TrimPrefix(commentBody, reReviewCmd)
	instructions := strings.TrimSpace(args)

	return ReReview, sanitizeInstructions(instructions), nil
}

// ImplementEventFromIssueComment transforms a GitHub IssueCommentEvent on an issue
// (not a PR) into a GitHubEvent for the /implement command.
// This is used to trigger autonomous agent implementation of issues.
func ImplementEventFromIssueComment(event *github.IssueCommentEvent) (*GitHubEvent, error) {
	// Only process issues, not PRs
	if event.GetIssue().IsPullRequest() {
		return nil, fmt.Errorf("comment is on a pull request, not an issue")
	}

	commentBody := strings.TrimSpace(strings.ToLower(event.GetComment().GetBody()))
	if !isImplementCommand(commentBody) {
		return nil, fmt.Errorf("comment is not an /implement command")
	}

	repo := event.GetRepo()
	if repo == nil || repo.GetOwner() == nil || repo.GetOwner().GetLogin() == "" || repo.GetName() == "" {
		return nil, fmt.Errorf("repository or owner information is missing from the event")
	}

	issueNumber := event.GetIssue().GetNumber()
	if issueNumber <= 0 {
		return nil, fmt.Errorf("invalid issue number: %d", issueNumber)
	}

	if event.GetComment().GetUser() == nil || event.GetComment().GetUser().GetLogin() == "" {
		return nil, fmt.Errorf("commenter information is missing from the event")
	}

	if event.GetInstallation() == nil || event.GetInstallation().GetID() == 0 {
		return nil, fmt.Errorf("installation ID is missing from the event")
	}

	// Extract instructions after /implement
	instructions := parseImplementInstructions(commentBody)

	return &GitHubEvent{
		Type:             ImplementIssue,
		RepoOwner:        repo.GetOwner().GetLogin(),
		RepoName:         repo.GetName(),
		RepoFullName:     repo.GetFullName(),
		RepoCloneURL:     repo.GetCloneURL(),
		Language:         repo.GetLanguage(),
		InstallationID:   event.GetInstallation().GetID(),
		IssueNumber:      issueNumber,
		IssueTitle:       event.GetIssue().GetTitle(),
		IssueBody:        event.GetIssue().GetBody(),
		UserInstructions: instructions,
		Commenter:        event.GetComment().GetUser().GetLogin(),
	}, nil
}

func isImplementCommand(commentBody string) bool {
	if commentBody == "/implement" {
		return true
	}
	// Allow "/implement " with instructions
	return strings.HasPrefix(commentBody, "/implement ")
}

func parseImplementInstructions(commentBody string) string {
	if !strings.HasPrefix(commentBody, "/implement ") {
		return ""
	}
	instructions := strings.TrimPrefix(commentBody, "/implement")
	instructions = strings.TrimSpace(instructions)

	return sanitizeInstructions(instructions)
}

// EventFromPushEvent transforms a GitHub PushEvent on the default branch
// into a GitHubEvent for background RAG re-indexing. Only pushes that target
// the repository's default branch are converted; all other pushes are
// ignored by returning an error.
func EventFromPushEvent(event *github.PushEvent) (*GitHubEvent, error) {
	repo := event.GetRepo()
	if repo == nil || repo.GetOwner() == nil || repo.GetOwner().GetLogin() == "" || repo.GetName() == "" {
		return nil, fmt.Errorf("repository or owner information is missing from push event")
	}

	if event.GetInstallation() == nil || event.GetInstallation().GetID() == 0 {
		return nil, fmt.Errorf("installation ID is missing from push event")
	}

	return &GitHubEvent{
		Type:           Reindex,
		RepoOwner:      repo.GetOwner().GetLogin(),
		RepoName:       repo.GetName(),
		RepoFullName:   repo.GetFullName(),
		RepoCloneURL:   repo.GetCloneURL(),
		Language:       repo.GetLanguage(),
		InstallationID: event.GetInstallation().GetID(),
		HeadSHA:        event.GetAfter(),
	}, nil
}
