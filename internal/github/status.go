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
// It adds severity badges to comments and includes a statistical summary.
func (s *statusUpdater) PostStructuredReview(ctx context.Context, event *core.GitHubEvent, review *core.StructuredReview) error {
	var comments []DraftReviewComment
	for _, sug := range review.Suggestions {
		if sug.FilePath != "" && sug.LineNumber > 0 && sug.Comment != "" {
			formattedComment := formatInlineComment(sug)
			// Enforce sane line ordering: startLine must be <= LineNumber
			startLine := sug.StartLine
			if startLine == 0 || startLine > sug.LineNumber {
				// Log for observability if we are normalizing a weird range
				if startLine > sug.LineNumber {
					fmt.Printf("Warning: normalizing invalid range %s:%d-%d to single line %d\n", sug.FilePath, startLine, sug.LineNumber, sug.LineNumber)
				}
				startLine = sug.LineNumber // treat as single-line at sug.LineNumber
			}
			comments = append(comments, DraftReviewComment{
				Path:      sug.FilePath,
				Line:      sug.LineNumber,
				StartLine: startLine,
				Body:      formattedComment,
			})
		}
	}

	formattedSummary := formatReviewSummary(review)
	return s.client.CreateReview(ctx, event.RepoOwner, event.RepoName, event.PRNumber, event.HeadSHA, formattedSummary, comments)
}

// formatInlineComment generates a pull request comment with severity alerts and category metadata.
func formatInlineComment(sug core.Suggestion) string {
	if sug.FilePath == "" || sug.LineNumber <= 0 {
		return ""
	}

	var sb strings.Builder
	lines := writeCommentHeader(&sb, sug)
	writeCommentBody(&sb, lines, severityAlert(sug.Severity))

	return sb.String()
}

func writeCommentHeader(sb *strings.Builder, sug core.Suggestion) []string {
	severity := sug.Severity
	emoji := severityEmoji(severity)

	content := strings.TrimSpace(sug.Comment)
	// Strip double blockquotes if the model generated them
	content = strings.ReplaceAll(content, "\n> > ", "\n> ")
	content = strings.ReplaceAll(content, "\n> [!", "\n[! ")

	lines := strings.Split(content, "\n")

	// 1. Process Title
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "###") {
		title := strings.TrimPrefix(strings.TrimSpace(lines[0]), "###")
		fmt.Fprintf(sb, "### 🛡️ %s\n", strings.TrimSpace(title))
		lines = lines[1:]
	} else {
		sb.WriteString("### 🛡️ Code Review Finding\n")
	}

	// 2. Badge Line
	fmt.Fprintf(sb, "%s **%s**", emoji, severity)
	if sug.Category != "" {
		fmt.Fprintf(sb, " | _%s_", sug.Category)
	}
	sb.WriteString("\n\n")

	return lines
}

func writeCommentBody(sb *strings.Builder, lines []string, alertType string) {
	insideAlert := false
	inCodeBlock := false

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// 1. Handle Code Blocks
		if strings.HasPrefix(trimmedLine, "```") {
			if inCodeBlock {
				inCodeBlock = false
			} else {
				if insideAlert {
					insideAlert = false
					sb.WriteString("\n")
				}
				inCodeBlock = true
			}
			sb.WriteString(line + "\n")
			continue
		}

		if inCodeBlock {
			sb.WriteString(line + "\n")
			continue
		}

		// 2. Handle Sub-Headers
		if strings.HasPrefix(trimmedLine, "####") {
			if insideAlert {
				insideAlert = false
				sb.WriteString("\n")
			}
			sb.WriteString(line + "\n")
			continue
		}

		// 3. Render Alert Content
		insideAlert = renderAlertLine(sb, line, trimmedLine, insideAlert, alertType)
	}
}

func renderAlertLine(sb *strings.Builder, line, trimmed string, insideAlert bool, alertType string) bool {
	if !insideAlert && trimmed != "" {
		fmt.Fprintf(sb, "> [!%s]\n", alertType)
		insideAlert = true
	}

	if insideAlert {
		strippedLine := strings.TrimPrefix(line, ">")
		cleanLine := strings.TrimRight(strippedLine, " \t\r\n")

		if cleanLine == "" && trimmed != "" {
			sb.WriteString("> \n")
		} else {
			fmt.Fprintf(sb, "> %s\n", cleanLine)
		}
	} else {
		sb.WriteString(line + "\n")
	}
	return insideAlert
}

// formatReviewSummary generates the final review summary including issue statistics.
func formatReviewSummary(review *core.StructuredReview) string {
	// Count severities
	counts := map[string]int{"Critical": 0, "High": 0, "Medium": 0, "Low": 0}
	for _, sug := range review.Suggestions {
		counts[sug.Severity]++
	}

	var sb strings.Builder
	if review.Title != "" {
		sb.WriteString(fmt.Sprintf("## %s\n\n", review.Title))
	} else {
		sb.WriteString("## 🔍 Code Review Summary\n\n")
	}

	// Add Verdict with Icon
	if review.Verdict != "" {
		icon := verdictIcon(review.Verdict)
		sb.WriteString(fmt.Sprintf("### %s Verdict: %s\n\n", icon, review.Verdict))
	}

	sb.WriteString(review.Summary)
	sb.WriteString("\n\n")

	if len(review.Suggestions) > 0 {
		sb.WriteString("\n---\n\n")
		sb.WriteString("### 📊 Issue Statistics\n")
		sb.WriteString("| Severity | Count |\n")
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

// verdictIcon returns an icon for the given verdict using normalized exact matching.
func verdictIcon(verdict string) string {
	v := strings.ToUpper(strings.TrimSpace(verdict))
	switch v {
	case "APPROVE":
		return "✅"
	case "REQUEST_CHANGES", "REQUEST CHANGES":
		return "🚫"
	case "COMMENT":
		return "💬"
	default:
		return "📝"
	}
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

// severityAlert returns the GitHub Alert type (NOTE, TIP, IMPORTANT, WARNING, CAUTION) for a severity.
func severityAlert(severity string) string {
	switch severity {
	case "Critical":
		return "CAUTION"
	case "High":
		return "WARNING"
	case "Medium":
		return "IMPORTANT"
	case "Low":
		return "NOTE"
	default:
		return "NOTE"
	}
}
