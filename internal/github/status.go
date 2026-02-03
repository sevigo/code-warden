// Package github provides functionality for interacting with the GitHub API.
package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v73/github"

	"github.com/sevigo/code-warden/internal/core"
)

// StatusUpdater defines the contract for updating the status of a GitHub Check Run
// and posting comments on pull requests.
type StatusUpdater interface {
	InProgress(ctx context.Context, event *core.GitHubEvent, title, summary string) (int64, error)
	Completed(ctx context.Context, event *core.GitHubEvent, checkRunID int64, conclusion, title, summary string) error
	PostStructuredReview(ctx context.Context, event *core.GitHubEvent, review *core.StructuredReview) error
	PostSimpleComment(ctx context.Context, event *core.GitHubEvent, body string) error
}

type statusUpdater struct {
	client Client
}

// NewStatusUpdater creates and returns a new instance of a statusUpdater.
func NewStatusUpdater(client Client) StatusUpdater {
	return &statusUpdater{client: client}
}

// PostSimpleComment posts a single, general comment on the pull request.
func (s *statusUpdater) PostSimpleComment(ctx context.Context, event *core.GitHubEvent, body string) error {
	return s.client.CreateComment(ctx, event.RepoOwner, event.RepoName, event.PRNumber, body)
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

// PostStructuredReview posts a new pull request review with line-specific comments.
// It formats comments with severity badges and posts a nicely formatted summary.
func (s *statusUpdater) PostStructuredReview(ctx context.Context, event *core.GitHubEvent, review *core.StructuredReview) error {
	var comments []DraftReviewComment
	for _, sug := range review.Suggestions {
		if sug.FilePath != "" && sug.LineNumber > 0 && sug.Comment != "" {
			formattedComment := formatInlineComment(sug)
			comments = append(comments, DraftReviewComment{
				Path: sug.FilePath,
				Line: sug.LineNumber,
				Body: formattedComment,
			})
		}
	}

	formattedSummary := formatReviewSummary(review)
	return s.client.CreateReview(ctx, event.RepoOwner, event.RepoName, event.PRNumber, event.HeadSHA, formattedSummary, comments)
}

// formatInlineComment creates a nicely formatted comment with severity and category.
func formatInlineComment(sug core.Suggestion) string {
	severity := sug.Severity
	emoji := severityEmoji(severity)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s **[%s]**", emoji, severity))
	if sug.Category != "" {
		sb.WriteString(fmt.Sprintf(" | _%s_", sug.Category))
	}
	sb.WriteString("\n\n")
	sb.WriteString(sug.Comment)
	return sb.String()
}

// formatReviewSummary creates a nicely formatted summary with statistics.
func formatReviewSummary(review *core.StructuredReview) string {
	// Count severities
	counts := map[string]int{"Critical": 0, "High": 0, "Medium": 0, "Low": 0}
	for _, sug := range review.Suggestions {
		counts[sug.Severity]++
	}

	var sb strings.Builder
	sb.WriteString("## 🔍 Code Review Summary\n\n")
	sb.WriteString(review.Summary)
	sb.WriteString("\n\n")

	if len(review.Suggestions) > 0 {
		sb.WriteString("---\n\n")
		sb.WriteString("### 📊 Issue Summary\n")
		sb.WriteString(fmt.Sprintf("| Severity | Count |\n"))
		sb.WriteString("|----------|-------|\n")
		if counts["Critical"] > 0 {
			sb.WriteString(fmt.Sprintf("| 🔴 Critical | %d |\n", counts["Critical"]))
		}
		if counts["High"] > 0 {
			sb.WriteString(fmt.Sprintf("| 🟠 High | %d |\n", counts["High"]))
		}
		if counts["Medium"] > 0 {
			sb.WriteString(fmt.Sprintf("| 🟡 Medium | %d |\n", counts["Medium"]))
		}
		if counts["Low"] > 0 {
			sb.WriteString(fmt.Sprintf("| 🟢 Low | %d |\n", counts["Low"]))
		}
	}

	return sb.String()
}

// severityEmoji returns an emoji for the given severity level.
func severityEmoji(severity string) string {
	switch severity {
	case "Critical":
		return "🔴"
	case "High":
		return "🟠"
	case "Medium":
		return "🟡"
	case "Low":
		return "🟢"
	default:
		return "⚪"
	}
}
