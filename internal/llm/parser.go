package llm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

var (
	// Matches: ## Suggestion [path/to/file.go:123] or ## Suggestion [path/to/file.go: 123]
	// Uses greedy .+ to match until the LAST colon, so Windows paths like C:\src\main.go:123 work.
	// suggestionHeaderRegex is removed in favor of manual parsing (parseSuggestionHeader)
	// to prevent ReDoS and support Windows paths (which contain colons).
	severityRegex = regexp.MustCompile(`(?i)\*\*Severity:?\*\*\s*(.*)`)
	categoryRegex = regexp.MustCompile(`(?i)\*\*Category:?\*\*\s*(.*)`)
)

// parseSuggestionHeader extracts file path and line number from a suggestion header.
// Format: "## Suggestion [path/to/file.go:123]"
// Standardizes on manual parsing to be 1. ReDoS-proof (linear time) and 2. Windows-path friendly.
func parseSuggestionHeader(line string) (string, int, bool) {
	const prefix = "## Suggestion ["
	if !strings.HasPrefix(line, prefix) && !strings.HasPrefix(line, "## SUGGESTION [") {
		// Case-insensitive check for prefix is a bit trickier without regex, but LLMs generally match case.
		// Let's do a quick Lower() check if exact match fails, or just strict prefix if we trust LLM constant output.
		// The regex was (?i), so we should be case-insensitive.
		if !strings.HasPrefix(strings.ToLower(line), strings.ToLower(prefix)) {
			return "", 0, false
		}
	}

	// Determine the actual casing of the prefix used, to slice correctly
	// Actually, just find the first '[' and start from there?
	// The prefix "## Suggestion [" len is 15.

	lowerLine := strings.ToLower(line)
	lowerPrefix := strings.ToLower(prefix)
	if !strings.HasPrefix(lowerLine, lowerPrefix) {
		return "", 0, false
	}

	// Find closing bracket
	closingIdx := strings.LastIndex(line, "]")
	if closingIdx == -1 || closingIdx <= len(prefix) {
		return "", 0, false
	}

	content := line[len(prefix):closingIdx]
	// Handle potential whitespace after prefix if LLM adds it? strict prefix len might cut into content if spacing varies.
	// Current implementation assumes single space.
	// Regexp was `##\s+Suggestion\s+`.
	// We should be robust.

	// Robust finding of content start:
	startIdx := strings.Index(line, "[")
	if startIdx == -1 {
		return "", 0, false
	}
	content = line[startIdx+1 : closingIdx]

	if content == "" {
		return "", 0, false
	}

	// Split on the *last* colon to get path and line.
	// This works for Windows paths (C:\foo.go:123) because the drive colon is earlier.
	lastColon := strings.LastIndex(content, ":")
	if lastColon == -1 {
		return "", 0, false
	}

	filePath := strings.TrimSpace(content[:lastColon])
	lineStr := strings.TrimSpace(content[lastColon+1:])

	lineNum, err := strconv.Atoi(lineStr)
	if err != nil {
		return "", 0, false
	}

	// Basic sanitization
	if strings.ContainsAny(filePath, "\x00\r\n") {
		return "", 0, false
	}

	return filePath, lineNum, true
}

// ParseError represents a failure to parse the LLM output into a structured format.
type ParseError struct {
	RawContent string
	Err        error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("failed to parse LLM output: %v", e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// parseMarkdownReview extracts structured review data from the LLM's Markdown output.
func parseMarkdownReview(markdown string) (*core.StructuredReview, error) {
	// 1. Normalize line endings for cross-platform reliability (Fixes High severity issue)
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")

	// 2. Strip wrapping markdown code fence safely (Fixes Critical panic risk)
	markdown = stripMarkdownFence(markdown)

	review := &core.StructuredReview{}
	lines := strings.Split(markdown, "\n")

	var currentSection string
	var currentSuggestion *core.Suggestion
	var commentBuilder strings.Builder
	var summaryBuilder strings.Builder

	// Use range loop for modern Go style (Fixes intrange linter error)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		upperLine := strings.ToUpper(line)

		// Top-level section headers (Fixes ifElseChain linter error by using switch)
		switch {
		case strings.HasPrefix(upperLine, "# REVIEW SUMMARY"):
			flushSuggestion(review, currentSuggestion, &commentBuilder)
			currentSuggestion = nil
			currentSection = "SUMMARY"
			continue
		case strings.HasPrefix(upperLine, "# VERDICT"):
			flushSuggestion(review, currentSuggestion, &commentBuilder)
			currentSuggestion = nil
			currentSection = "VERDICT"
			continue
		case strings.HasPrefix(upperLine, "# SUGGESTIONS"):
			flushSuggestion(review, currentSuggestion, &commentBuilder)
			currentSuggestion = nil
			currentSection = "SUGGESTIONS"
			continue
		case strings.HasPrefix(upperLine, "## SUGGESTION"):
			// Flush previous suggestion
			flushSuggestion(review, currentSuggestion, &commentBuilder)

			// Parse new suggestion header manually
			filePath, lineNum, ok := parseSuggestionHeader(line)
			if ok {
				currentSuggestion = &core.Suggestion{
					FilePath:   filePath,
					LineNumber: lineNum,
				}
			} else {
				currentSuggestion = &core.Suggestion{FilePath: "unknown"}
			}
			currentSection = "SUGGESTION_CONTENT"
			continue
		}

		// Content parsing based on section
		switch currentSection {
		case "SUMMARY":
			processSummaryLine(line, &summaryBuilder)
		case "VERDICT":
			processVerdictLine(line, review, &summaryBuilder)
			currentSection = "DONE_VERDICT"
		case "SUGGESTION_CONTENT":
			processSuggestionLine(rawLine, currentSuggestion, &commentBuilder)
		}
	}

	// Flush remaining state
	if summaryBuilder.Len() > 0 && review.Summary == "" {
		review.Summary = summaryBuilder.String()
	}
	flushSuggestion(review, currentSuggestion, &commentBuilder)

	if review.Summary == "" && len(review.Suggestions) == 0 {
		return nil, fmt.Errorf("failed to parse review: no recognized sections found")
	}

	return review, nil
}

func processSummaryLine(line string, builder *strings.Builder) {
	if line != "" && !strings.HasPrefix(line, "#") {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(line)
	}
}

func processVerdictLine(line string, review *core.StructuredReview, builder *strings.Builder) {
	if line != "" && !strings.HasPrefix(line, "#") {
		review.Verdict = line // Critical: preserve structured field
		verdictPrefix := "**Verdict:** " + line
		if builder.Len() > 0 {
			review.Summary = builder.String() + "\n\n" + verdictPrefix
		} else {
			review.Summary = verdictPrefix
		}
	}
}

func processSuggestionLine(rawLine string, s *core.Suggestion, builder *strings.Builder) {
	if s == nil {
		return
	}
	line := strings.TrimSpace(rawLine)

	switch {
	case strings.HasPrefix(line, "**Severity"):
		if matches := severityRegex.FindStringSubmatch(line); len(matches) > 1 {
			s.Severity = strings.TrimSpace(matches[1])
		}
	case strings.HasPrefix(line, "**Category"):
		if matches := categoryRegex.FindStringSubmatch(line); len(matches) > 1 {
			s.Category = strings.TrimSpace(matches[1])
		}
	case strings.HasPrefix(line, "### Comment"):
		// Skip header
	case strings.HasPrefix(line, "### Rationale"):
		builder.WriteString("\n\n**Rationale:**\n")
	case strings.HasPrefix(line, "### Fix"):
		builder.WriteString("\n\n**Fix:**\n")
	default:
		if line != "" || builder.Len() > 0 {
			builder.WriteString(rawLine + "\n")
		}
	}
}

// flushSuggestion appends the current suggestion (if any) to the review and resets the builder.
func flushSuggestion(review *core.StructuredReview, s *core.Suggestion, builder *strings.Builder) {
	if s == nil {
		return
	}
	if builder.Len() > 0 {
		s.Comment = strings.TrimSpace(builder.String())
		builder.Reset()
	}
	review.Suggestions = append(review.Suggestions, *s)
}

// stripMarkdownFence removes ```markdown ... ``` wrapping that some LLMs add around their output.
// It is hardened against missing closing fences to prevent panics, and only strips
// fences if they are explicitly "markdown", "md", or have no language specified.
func stripMarkdownFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return s
	}

	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return s
	}

	// Validate opening fence and check language
	firstLine := strings.ToLower(strings.TrimSpace(lines[0]))
	if !strings.HasPrefix(firstLine, "```") {
		return s
	}

	lang := strings.TrimPrefix(firstLine, "```")
	if lang != "" && lang != "markdown" && lang != "md" {
		// Not a markdown fence, preserve it
		return s
	}

	// Find closing fence anywhere after first line (scanning backwards)
	// This handles cases where LLMs add trailing text after the json/markdown block.
	closeIdx := -1
	for i := len(lines) - 1; i > 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			closeIdx = i
			break
		}
	}

	if closeIdx > 0 {
		return strings.TrimSpace(strings.Join(lines[1:closeIdx], "\n"))
	}

	// Fallback: If closing fence is missing (truncation), return everything after the opening fence
	return strings.TrimSpace(strings.Join(lines[1:], "\n"))
}
