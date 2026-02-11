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
	// Also handles Windows paths like C:\path\to\file.go by using a greedy match up to the last colon.
	suggestionHeaderRegex = regexp.MustCompile(`(?i)##\s+Suggestion\s+\[(.*?):\s*(\d+)\]`)
	severityRegex         = regexp.MustCompile(`(?i)\*\*Severity:?\*\*\s*(.*)`)
	categoryRegex         = regexp.MustCompile(`(?i)\*\*Category:?\*\*\s*(.*)`)
)

// parseMarkdownReview extracts structured review data from the LLM's Markdown output.
// It handles several common LLM quirks:
// - Response wrapped in ```markdown ... ``` fences
// - Inconsistent heading levels or casing
// - Missing sections (only Summary is strictly required)
func parseMarkdownReview(markdown string) (*core.StructuredReview, error) {
	// Strip wrapping markdown code fence if the LLM included one
	markdown = stripMarkdownFence(markdown)

	review := &core.StructuredReview{}
	lines := strings.Split(markdown, "\n")

	var currentSection string
	var currentSuggestion *core.Suggestion
	var commentBuilder strings.Builder
	var summaryBuilder strings.Builder

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])

		// Top-level section headers (case-insensitive)
		upperLine := strings.ToUpper(line)
		if strings.HasPrefix(upperLine, "# REVIEW SUMMARY") {
			flushSuggestion(review, currentSuggestion, &commentBuilder)
			currentSuggestion = nil
			currentSection = "SUMMARY"
			continue
		} else if strings.HasPrefix(upperLine, "# VERDICT") {
			flushSuggestion(review, currentSuggestion, &commentBuilder)
			currentSuggestion = nil
			currentSection = "VERDICT"
			continue
		} else if strings.HasPrefix(upperLine, "# SUGGESTIONS") {
			flushSuggestion(review, currentSuggestion, &commentBuilder)
			currentSuggestion = nil
			currentSection = "SUGGESTIONS"
			continue
		}

		// Suggestion sub-headers
		if strings.HasPrefix(line, "## Suggestion") || strings.HasPrefix(line, "## suggestion") {
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
				// Header exists but regex didn't match — keep the suggestion with best-effort info
				currentSuggestion = &core.Suggestion{
					FilePath: "unknown",
				}
			}
			currentSection = "SUGGESTION_CONTENT"
			continue
		}

		// Content parsing based on section
		switch currentSection {
		case "SUMMARY":
			if line != "" && !strings.HasPrefix(line, "#") {
				if summaryBuilder.Len() > 0 {
					summaryBuilder.WriteString("\n")
				}
				summaryBuilder.WriteString(line)
			}
		case "VERDICT":
			if line != "" && !strings.HasPrefix(line, "#") {
				review.Summary = summaryBuilder.String()
				if review.Summary != "" {
					review.Summary += "\n\n**Verdict:** " + line
				} else {
					review.Summary = "**Verdict:** " + line
				}
				// Rebuild summaryBuilder with the updated summary
				summaryBuilder.Reset()
				summaryBuilder.WriteString(review.Summary)
				currentSection = "DONE_VERDICT"
			}
		case "SUGGESTION_CONTENT":
			if currentSuggestion == nil {
				continue
			}

			// Metadata parsing
			if strings.HasPrefix(line, "**Severity") {
				matches := severityRegex.FindStringSubmatch(line)
				if len(matches) > 1 {
					currentSuggestion.Severity = strings.TrimSpace(matches[1])
				}
				continue
			}
			if strings.HasPrefix(line, "**Category") {
				matches := categoryRegex.FindStringSubmatch(line)
				if len(matches) > 1 {
					currentSuggestion.Category = strings.TrimSpace(matches[1])
				}
				continue
			}

			// Sub-section headings inside a suggestion
			if strings.HasPrefix(line, "### Comment") {
				continue
			}
			if strings.HasPrefix(line, "### Rationale") {
				commentBuilder.WriteString("\n\n**Rationale:**\n")
				continue
			}
			if strings.HasPrefix(line, "### Fix") {
				commentBuilder.WriteString("\n\n**Fix:**\n")
				continue
			}

			// Accumulate content (preserve original indentation from lines[i])
			if line != "" || commentBuilder.Len() > 0 {
				commentBuilder.WriteString(lines[i] + "\n")
			}
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
func stripMarkdownFence(s string) string {
	trimmed := strings.TrimSpace(s)
	// Check for ```markdown or ```md at the beginning
	if strings.HasPrefix(trimmed, "```markdown") || strings.HasPrefix(trimmed, "```md") {
		// Find the end of the opening fence line
		idx := strings.Index(trimmed, "\n")
		if idx < 0 {
			return s
		}
		inner := trimmed[idx+1:]
		// Strip trailing ``` (with possible whitespace around it)
		if lastFence := strings.LastIndex(inner, "```"); lastFence >= 0 {
			inner = inner[:lastFence]
		}
		return strings.TrimSpace(inner)
	}
	return s
}
