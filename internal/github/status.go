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
		// Context check at start of loop iteration for responsiveness
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if sug.FilePath == "" || sug.LineNumber <= 0 || sug.Comment == "" {
			continue
		}

		formattedComment := formatInlineComment(ctx, sug)
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

// formatInlineComment creates a GitHub-flavored markdown comment for inline review suggestions.
// It uses GitHub Alerts for Critical/High severity and plain markdown for Medium/Low.
func formatInlineComment(ctx context.Context, sug core.Suggestion) string {
	if ctx.Err() != nil {
		return ""
	}

	// Validate required fields
	if sug.LineNumber <= 0 || strings.TrimSpace(sug.Comment) == "" {
		return ""
	}

	var sb strings.Builder

	// 1. Severity Header
	emoji := SeverityEmoji(sug.Severity)
	fmt.Fprintf(&sb, "**%s %s**", emoji, sug.Severity)
	if sug.Category != "" {
		fmt.Fprintf(&sb, " — %s", sug.Category)
	}
	sb.WriteString("\n\n")

	// 2. Process Comment
	comment := preprocessComment(sug.Comment)

	// 3. Wrap in GitHub Alert for Critical/High
	if shouldUseAlert(sug.Severity) {
		alertType := SeverityAlert(sug.Severity)
		sb.WriteString(fmt.Sprintf("> [!%s]\n", alertType))

		// Extract text before first code block
		textPart, codePart := splitTextAndCode(comment)

		// Quote the text part
		quotedText := quoteText(textPart)
		sb.WriteString(quotedText)

		// Add code blocks outside the alert
		if codePart != "" {
			sb.WriteString("\n\n")
			sb.WriteString(codePart)
		}
	} else {
		// Medium/Low: Plain markdown (no alert)
		sb.WriteString(comment)
	}

	// 4. Add Code Suggestion (if present) - MUST be outside alert
	if sug.CodeSuggestion != "" {
		sb.WriteString("\n\n```suggestion\n")
		sb.WriteString(dedent(sug.CodeSuggestion))
		sb.WriteString("\n```")
	}

	// 5. Add Source Citation (anti-hallucination grounding)
	if sug.Source != "" {
		sb.WriteString("\n\n")
		fmt.Fprintf(&sb, "*📍 Source: `%s`*", sug.Source)
	}

	return sb.String()
}

// preprocessComment cleans up LLM-generated comments by:
// - Stripping trailing whitespace from each line (fixes markdown rendering)
// - Stripping legacy ### title headers
// - Converting #### headers to bold with emojis
func preprocessComment(comment string) string {
	comment = strings.TrimSpace(comment)
	lines := strings.Split(comment, "\n")
	var processed []string

	for i := range lines {
		line := lines[i]
		// Strip trailing whitespace from each line (fixes markdown rendering issues
		line = strings.TrimRight(line, " \t")
		trimmed := strings.TrimSpace(line)

		// Strip legacy ### headers (e.g., "### Old Style Title")
		if strings.HasPrefix(trimmed, "### ") {
			continue
		}

		// Convert #### headers to bold with emoji
		if strings.HasPrefix(trimmed, "#### ") {
			headerText := strings.TrimSpace(strings.TrimPrefix(trimmed, "#### "))
			headerText = strings.TrimSpace(strings.TrimPrefix(headerText, "**"))
			headerText = strings.TrimSpace(strings.TrimSuffix(headerText, "**"))

			// Simplify common patterns: "Suggested Fix" → "Fix"
			headerText = strings.TrimPrefix(headerText, "Suggested ")
			headerText = strings.TrimPrefix(headerText, "Recommended ")
			headerText = strings.TrimPrefix(headerText, "Proposed ")

			// Map common header patterns to emojis
			emoji := "💡"
			switch {
			case containsAny(strings.ToLower(headerText), []string{"fix", "solution", "recommendation"}):
				emoji = "💡"
			case containsAny(strings.ToLower(headerText), []string{"rationale", "why", "reason"}):
				emoji = "📖"
			case containsAny(strings.ToLower(headerText), []string{"observation", "issue", "problem"}):
				emoji = "🔍"
			}

			processed = append(processed, fmt.Sprintf("%s **%s:**", emoji, headerText))
			continue
		}

		processed = append(processed, line)
	}

	return strings.Join(processed, "\n")
}

// shouldUseAlert determines if a severity level should use GitHub Alerts
func shouldUseAlert(_ string) bool {
	return false
	// switch severity {
	// case SeverityCritical, SeverityHigh:
	//	return true
	// default:
	//	return false
	// }
}

// splitTextAndCode separates text content from code blocks
func splitTextAndCode(content string) (text, code string) {
	// Find first code block
	codeStart := strings.Index(content, "```")
	if codeStart == -1 {
		return content, ""
	}

	return strings.TrimSpace(content[:codeStart]), strings.TrimSpace(content[codeStart:])
}

// quoteText adds "> " prefix to each line for GitHub alert formatting
func quoteText(text string) string {
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	var quoted []string
	for _, line := range lines {
		quoted = append(quoted, "> "+line)
	}
	return strings.Join(quoted, "\n")
}

// containsAny checks if text contains any of the given substrings
func containsAny(text string, substrs []string) bool {
	for _, substr := range substrs {
		if strings.Contains(text, substr) {
			return true
		}
	}
	return false
}

// formatReviewSummary creates a summary comment for the entire PR review
func formatReviewSummary(review *core.StructuredReview) string {
	var sb strings.Builder

	// Title
	title := review.Title
	if title == "" {
		title = "🔍 Code Review Summary"
	}
	sb.WriteString(fmt.Sprintf("## %s\n\n", title))

	// Verdict
	icon := verdictIcon(review.Verdict)
	fmt.Fprintf(&sb, "### %s Verdict: %s\n\n", icon, review.Verdict)

	// Summary content
	if review.Summary != "" {
		summary := preprocessComment(review.Summary)
		sb.WriteString(summary)
		sb.WriteString("\n\n")
	}

	// Compact statistics (only if suggestions exist)
	if len(review.Suggestions) > 0 {
		stats := buildCompactStats(review.Suggestions)
		sb.WriteString(stats)
	}

	sb.WriteString("\n\n---\n")
	sb.WriteString("> 💡 Reply with `/rereview` to trigger a new review.")

	return sb.String()
}

// buildCompactStats creates a one-line summary of issue counts by severity
func buildCompactStats(suggestions []core.Suggestion) string {
	counts := make(map[string]int)
	for _, sug := range suggestions {
		severity := sug.Severity
		if severity == "" {
			severity = "Unknown"
		}
		counts[severity]++
	}

	total := len(suggestions)
	var parts []string

	// Order: Critical, High, Medium, Low
	if count := counts[SeverityCritical]; count > 0 {
		parts = append(parts, fmt.Sprintf("%s %d %s", SeverityEmojiCritical, count, SeverityCritical))
	}
	if count := counts[SeverityHigh]; count > 0 {
		parts = append(parts, fmt.Sprintf("%s %d %s", SeverityEmojiHigh, count, SeverityHigh))
	}
	if count := counts[SeverityMedium]; count > 0 {
		parts = append(parts, fmt.Sprintf("%s %d %s", SeverityEmojiMedium, count, SeverityMedium))
	}
	if count := counts[SeverityLow]; count > 0 {
		parts = append(parts, fmt.Sprintf("%s %d %s", SeverityEmojiLow, count, SeverityLow))
	}

	if len(parts) == 0 {
		return ""
	}

	return fmt.Sprintf("*Found %d suggestion(s): %s*\n\n", total, strings.Join(parts, ", "))
}

// SeverityEmoji returns the emoji for a given severity level
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

// SeverityAlert returns the GitHub Alert type for a severity level
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

// verdictIcon returns the emoji for a verdict
func verdictIcon(verdict string) string {
	v := strings.ToUpper(strings.TrimSpace(verdict))
	switch v {
	case "APPROVE", "APPROVED":
		return VerdictIconApprove
	case "REQUEST_CHANGES", "CHANGES_REQUESTED", "REQUEST CHANGES":
		return VerdictIconRequestChanges
	case "COMMENT", "NEEDS_DISCUSSION":
		return VerdictIconComment
	default:
		return "📝"
	}
}

// dedent removes common leading whitespace from all lines in s.
// This ensures that multi-line code blocks or suggestions are properly
// aligned when rendered in GitHub.
func dedent(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	lines = trimEmptyLines(lines)
	if len(lines) == 0 {
		return ""
	}

	minIndent := findMinIndent(lines)
	if minIndent <= 0 {
		return strings.Join(lines, "\n")
	}

	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}
	return strings.Join(lines, "\n")
}

func trimEmptyLines(lines []string) []string {
	var start int
	found := false
	for i := range lines {
		if strings.TrimSpace(lines[i]) != "" {
			start = i
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	end := len(lines)
	for i := len(lines); i > start; i-- {
		if strings.TrimSpace(lines[i-1]) != "" {
			end = i
			break
		}
	}
	return lines[start:end]
}

func findMinIndent(lines []string) int {
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := 0
		for _, r := range line {
			if r == ' ' || r == '\t' {
				indent++
			} else {
				break
			}
		}
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	return minIndent
}
