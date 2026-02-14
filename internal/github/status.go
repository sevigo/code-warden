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
			if formattedComment == "" {
				continue
			}
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

	// Compact Header: ### 🔴 Critical | Security | Title
	// If no title is present, use "Code Review Finding"
	writeCompactHeader(&sb, sug)

	// Body: Render the comment body, converting #### to bold and handling alerts
	writeCompactBody(&sb, sug.Comment, sug.Severity)

	return sb.String()
}

func writeCompactHeader(sb *strings.Builder, sug core.Suggestion) {
	emoji := severityEmoji(sug.Severity)
	title := "Code Review Finding"

	// Extract title from comment if present (lines starting with ###)
	lines := strings.Split(sug.Comment, "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "###") {
		title = strings.TrimPrefix(strings.TrimSpace(lines[0]), "###")
		title = strings.TrimSpace(title)
	}

	fmt.Fprintf(sb, "### %s %s", emoji, sug.Severity)
	if sug.Category != "" {
		fmt.Fprintf(sb, " | %s", sug.Category)
	}
	fmt.Fprintf(sb, " | %s\n\n", title)
}

func writeCompactBody(sb *strings.Builder, comment, severity string) {
	// Strip the title line if we extracted it
	lines := strings.Split(comment, "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "###") {
		lines = lines[1:]
	}

	alertType := severityAlert(severity)
	state := &commentState{}

	for _, line := range lines {
		processCommentLine(sb, line, state, alertType)
	}
}

type commentState struct {
	insideAlert bool
	inCodeBlock bool
}

func processCommentLine(sb *strings.Builder, line string, state *commentState, alertType string) {
	trimmedLine := strings.TrimSpace(line)

	// Skip empty lines at the start
	if sb.Len() == 0 && trimmedLine == "" {
		return
	}

	// 1. Handle Code Blocks
	if strings.HasPrefix(trimmedLine, "```") {
		if state.inCodeBlock {
			state.inCodeBlock = false
		} else {
			if state.insideAlert {
				state.insideAlert = false
				// Close alert implicitly by newline
			}
			state.inCodeBlock = true
		}
		sb.WriteString(line + "\n")
		return
	}

	if state.inCodeBlock {
		sb.WriteString(line + "\n")
		return
	}

	// 2. Handle Sub-Headers (convert #### to bold)
	if strings.HasPrefix(trimmedLine, "####") {
		if state.insideAlert {
			state.insideAlert = false
			sb.WriteString("\n")
		}
		// Convert "#### Observation" to "**Observation**"
		headerContent := strings.TrimPrefix(trimmedLine, "####")
		fmt.Fprintf(sb, "**%s**\n", strings.TrimSpace(headerContent))
		return
	}

	// 3. Render Alert Content
	// We want to wrap the core content in an alert, but avoid double blockquotes
	// The original code was handling "> >". Let's simplify.
	// If the line is already a blockquote, strip one level.
	if strings.HasPrefix(trimmedLine, ">") {
		line = strings.TrimPrefix(line, ">")
		line = strings.TrimPrefix(line, " ")
	}

	state.insideAlert = renderAlertLine(sb, line, trimmedLine, state.insideAlert, alertType)
}

func renderAlertLine(sb *strings.Builder, line, trimmed string, insideAlert bool, alertType string) bool {
	if !insideAlert && trimmed != "" {
		fmt.Fprintf(sb, "> [!%s]\n", alertType)
		insideAlert = true
	}

	if insideAlert {
		if trimmed == "" {
			sb.WriteString(">\n")
		} else {
			fmt.Fprintf(sb, "> %s\n", line)
		}
	} else {
		// Should not be reached if logic is correct for "wrapping everything in alert"
		// But if we decide NOT to wrap everything, this handles it.
		// Current logic: we wrap the main body in the alert corresponding to severity.
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

	// 1. Verdict (Top Priority)
	if review.Verdict != "" {
		icon := verdictIcon(review.Verdict)
		// E.g., ### 🚫 Verdict: REQUEST_CHANGES
		sb.WriteString(fmt.Sprintf("### %s Verdict: %s\n\n", icon, review.Verdict))
	} else {
		sb.WriteString("### 📝 Code Review Summary\n\n")
	}

	// 2. Main Summary Body
	sb.WriteString(review.Summary)
	sb.WriteString("\n\n")

	// 3. Statistics Table (Only if suggestions exist)
	if len(review.Suggestions) > 0 {
		sb.WriteString("---\n")
		sb.WriteString("#### 📊 Issue Statistics\n\n")
		sb.WriteString("| Severity | Count |\n")
		sb.WriteString("|----------|-------|\n")

		// Order matters: Critical -> Low
		order := []string{"Critical", "High", "Medium", "Low"}
		for _, sev := range order {
			if count := counts[sev]; count > 0 {
				emoji := severityEmoji(sev)
				sb.WriteString(fmt.Sprintf("| %s %s | %d |\n", emoji, sev, count))
			}
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
