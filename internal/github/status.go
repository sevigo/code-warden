// Package github provides functionality for interacting with the GitHub API.
package github

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/go-github/v73/github"

	"github.com/sevigo/code-warden/internal/core"
)

// Severity emojis
const (
	SeverityEmojiCritical = "🔴"
	SeverityEmojiHigh     = "🟠"
	SeverityEmojiMedium   = "🟡"
	SeverityEmojiLow      = "🟢"
)

// Verdict icons
const (
	VerdictIconApprove        = "✅"
	VerdictIconRequestChanges = "🚫"
	VerdictIconComment        = "💬"
)

// Severity constants to avoid string duplication.
const (
	SeverityCritical = "Critical"
	SeverityHigh     = "High"
	SeverityMedium   = "Medium"
	SeverityLow      = "Low"
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
	logger *slog.Logger
}

// NewStatusUpdater creates and returns a new instance of a statusUpdater.
func NewStatusUpdater(client Client, logger *slog.Logger) StatusUpdater {
	return &statusUpdater{client: client, logger: logger}
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
		if sug.FilePath == "" || sug.LineNumber <= 0 || sug.Comment == "" {
			continue
		}
		formattedComment := formatInlineComment(sug)
		if formattedComment == "" {
			continue
		}
		// Enforce sane line ordering: startLine must be <= LineNumber
		startLine := sug.StartLine
		if startLine == 0 || startLine > sug.LineNumber {
			if startLine > sug.LineNumber {
				s.logger.Warn("normalizing invalid line range",
					"file", sug.FilePath,
					"start_line", startLine,
					"line_number", sug.LineNumber,
					"normalized_to", sug.LineNumber,
				)
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

	formattedSummary := formatReviewSummary(review)
	return s.client.CreateReview(ctx, event.RepoOwner, event.RepoName, event.PRNumber, event.HeadSHA, formattedSummary, comments)
}

// formatInlineComment generates a pull request comment with a clean, compact format.
func formatInlineComment(sug core.Suggestion) string {
	if sug.FilePath == "" || sug.LineNumber <= 0 {
		return ""
	}

	var sb strings.Builder
	lines := writeCommentHeader(&sb, sug)

	// Determine if we need an alert block
	prefix := ""
	if sug.Severity == SeverityCritical || sug.Severity == SeverityHigh {
		alert := SeverityAlert(sug.Severity)
		fmt.Fprintf(&sb, "> [!%s]\n", alert)
		prefix = "> "
	}

	writeCommentBody(&sb, lines, prefix)

	// Append GitHub Suggested Change if present
	// MUST be outside the alert block to function as a suggested change
	if sug.SuggestedCode != "" {
		sb.WriteString("\n```suggestion\n")
		// Sanitize to prevent breaking the fence
		code := strings.ReplaceAll(sug.SuggestedCode, "```", "`"+""+"`"+""+"`")
		sb.WriteString(strings.TrimSpace(code))
		sb.WriteString("\n```\n")
	}

	// Add Re-Review Footer only if not already present
	// Check original comment for the command to avoid duplication
	if !strings.Contains(sug.Comment, "/rereview") {
		sb.WriteString("\n---\n")
		sb.WriteString("> 💡 Reply with `/rereview` to trigger a new review.")
	}

	return sb.String()
}

func writeCommentHeader(sb *strings.Builder, sug core.Suggestion) []string {
	severity := sug.Severity
	emoji := SeverityEmoji(severity)

	content := strings.TrimSpace(sug.Comment)
	// Strip double blockquotes if the model generated them
	content = strings.TrimPrefix(content, "> > ")
	content = strings.ReplaceAll(content, "\n> > ", "\n> ")
	content = strings.ReplaceAll(content, "\n> [!", "\n[! ")

	lines := strings.Split(content, "\n")

	// Skip any ### title line (for backward compatibility with old prompts)
	startIdx := 0
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "###") {
		startIdx = 1
	}

	// Write compact header line: **🔴 Critical** — Category
	fmt.Fprintf(sb, "**%s %s**", emoji, severity)
	if sug.Category != "" {
		fmt.Fprintf(sb, " — %s", sug.Category)
	}
	sb.WriteString("\n\n")

	return lines[startIdx:]
}

func writeCommentBody(sb *strings.Builder, lines []string, prefix string) {
	inCodeBlock := false

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Handle Code Blocks
		if strings.HasPrefix(trimmedLine, "```") {
			inCodeBlock = !inCodeBlock
			sb.WriteString(prefix + line + "\n")
			continue
		}

		if inCodeBlock {
			sb.WriteString(prefix + line + "\n")
			continue
		}

		// Handle Sub-Headers (####) - convert to bold
		if strings.HasPrefix(trimmedLine, "####") {
			headerText := strings.TrimSpace(strings.TrimPrefix(trimmedLine, "####"))
			sb.WriteString(prefix + formatSubHeader(headerText))
			continue
		}

		// Skip ### headers (already handled in header)
		if strings.HasPrefix(trimmedLine, "###") {
			continue
		}

		// Write the line with prefix
		sb.WriteString(prefix + line + "\n")
	}
}

func formatSubHeader(headerText string) string {
	switch {
	case strings.Contains(headerText, "Suggested Fix"), strings.Contains(headerText, "Fix"):
		return "💡 **Fix:**\n"
	default:
		return "**" + headerText + "**\n"
	}
}

// formatReviewSummary generates the final review summary with a compact statistics line.
func formatReviewSummary(review *core.StructuredReview) string {
	// Count severities
	counts := map[string]int{
		SeverityCritical: 0,
		SeverityHigh:     0,
		SeverityMedium:   0,
		SeverityLow:      0,
	}
	total := 0
	for _, sug := range review.Suggestions {
		counts[sug.Severity]++
		total++
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

	// Compact statistics line instead of table
	if total > 0 {
		stats := buildStatsLine(counts)
		if len(stats) > 0 {
			sb.WriteString(fmt.Sprintf("*Found %d suggestion(s): %s*\n", total, strings.Join(stats, ", ")))
		}
	}

	return sb.String()
}

func buildStatsLine(counts map[string]int) []string {
	var stats []string
	if counts[SeverityCritical] > 0 {
		stats = append(stats, fmt.Sprintf("%s %d Critical", SeverityEmojiCritical, counts[SeverityCritical]))
	}
	if counts[SeverityHigh] > 0 {
		stats = append(stats, fmt.Sprintf("%s %d High", SeverityEmojiHigh, counts[SeverityHigh]))
	}
	if counts[SeverityMedium] > 0 {
		stats = append(stats, fmt.Sprintf("%s %d Medium", SeverityEmojiMedium, counts[SeverityMedium]))
	}
	if counts[SeverityLow] > 0 {
		stats = append(stats, fmt.Sprintf("🟢 %d Low", counts[SeverityLow]))
	}
	return stats
}

// verdictIcon returns an icon for the given verdict using normalized exact matching.
func verdictIcon(verdict string) string {
	v := strings.ToUpper(strings.TrimSpace(verdict))
	switch v {
	case "APPROVE":
		return VerdictIconApprove
	case "REQUEST_CHANGES", "REQUEST CHANGES":
		return VerdictIconRequestChanges
	case "COMMENT":
		return VerdictIconComment
	default:
		return "📝"
	}
}

// SeverityEmoji returns an emoji for the given severity level.
func SeverityEmoji(severity string) string {
	switch severity {
	case SeverityCritical:
		return SeverityEmojiCritical
	case SeverityHigh:
		return SeverityEmojiHigh
	case SeverityMedium:
		return SeverityEmojiMedium
	case SeverityLow:
		return SeverityEmojiLow
	default:
		return "⚪"
	}
}

// SeverityAlert returns the GitHub alert type for a severity level.
func SeverityAlert(severity string) string {
	switch severity {
	case SeverityCritical:
		return "CAUTION"
	case SeverityHigh:
		return "WARNING"
	case SeverityMedium:
		return "IMPORTANT"
	case SeverityLow:
		return "NOTE"
	default:
		return "NOTE"
	}
}
