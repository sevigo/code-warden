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
	suggestionHeaderRegex = regexp.MustCompile(`(?i)##\s+Suggestion\s+\[(.+):\s*(\d+)\]`)
	severityRegex         = regexp.MustCompile(`(?i)\*\*Severity:?\*\*\s*(.*)`)
	categoryRegex         = regexp.MustCompile(`(?i)\*\*Category:?\*\*\s*(.*)`)
)

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

			// Parse new suggestion header
			matches := suggestionHeaderRegex.FindStringSubmatch(line)
			if len(matches) == 3 {
				lineNum, _ := strconv.Atoi(matches[2])
				currentSuggestion = &core.Suggestion{
					FilePath:   strings.TrimSpace(matches[1]),
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

	// Check for closing fence on the last line
	lastIdx := len(lines) - 1
	if strings.TrimSpace(lines[lastIdx]) == "```" {
		return strings.TrimSpace(strings.Join(lines[1:lastIdx], "\n"))
	}

	// Fallback: If closing fence is missing (truncation), return everything after the opening fence
	return strings.TrimSpace(strings.Join(lines[1:], "\n"))
}
