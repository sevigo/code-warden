package llm

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// parseMarkdownReview parses a Markdown-formatted review into a StructuredReview.
// It expects a format like:
// # REVIEW SUMMARY
// ...
// # VERDICT
// ...
// # SUGGESTIONS
// ## Suggestion path/to/file.go:123
// **Severity:** ...
// **Category:** ...
// ### Comment
// ...
func parseMarkdownReview(raw string) (*core.StructuredReview, error) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	review := &core.StructuredReview{}
	var currentSuggestion *core.Suggestion
	var commentBuilder strings.Builder

	// States
	const (
		StateNone = iota
		StateSummary
		StateVerdict
		StateSuggestions
		StateInSuggestion
		StateInComment
	)
	state := StateNone

	// Regex for suggestion header: ## Suggestion path/to/file.go:123
	// Also handles cases with or without "Suggestion" prefix if the model gets creative,
	// but we'll try to enforce "## Suggestion" in the prompt.
	// We'll be slightly flexible: "## Suggestion <path>:<line>"
	reSuggestion := regexp.MustCompile(`^##\s+Suggestion\s+(.+?):(\d+)$`)
	reSeverity := regexp.MustCompile(`^\*\*Severity:\*\*\s*(.+)$`)
	reCategory := regexp.MustCompile(`^\*\*Category:\*\*\s*(.+)$`)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Header detection
		if strings.HasPrefix(line, "# REVIEW SUMMARY") {
			state = StateSummary
			continue
		}
		if strings.HasPrefix(line, "# VERDICT") {
			state = StateVerdict
			continue
		}
		if strings.HasPrefix(line, "# SUGGESTIONS") {
			state = StateSuggestions
			continue
		}

		// Sub-header detection (Suggestions)
		if strings.HasPrefix(line, "## Suggestion") {
			// Finalize previous comment if exists
			if currentSuggestion != nil && state == StateInComment {
				currentSuggestion.Comment = strings.TrimSpace(commentBuilder.String())
				review.Suggestions = append(review.Suggestions, *currentSuggestion)
				commentBuilder.Reset()
			}

			matches := reSuggestion.FindStringSubmatch(line)
			if len(matches) == 3 {
				lineNum, _ := strconv.Atoi(matches[2])
				currentSuggestion = &core.Suggestion{
					FilePath:   strings.TrimSpace(matches[1]),
					LineNumber: lineNum,
				}
				state = StateInSuggestion
			}
			continue
		}

		if strings.HasPrefix(line, "### Comment") {
			state = StateInComment
			continue
		}

		// Content handling based on state
		switch state {
		case StateSummary:
			if trimmed != "" || review.Summary != "" { // Preserve newlines in summary
				if review.Summary != "" {
					review.Summary += "\n"
				}
				review.Summary += line
			}
		case StateVerdict:
			if trimmed != "" {
				// Take the first non-empty line as verdict
				if review.Verdict == "" {
					review.Verdict = trimmed
				}
			}
		case StateInSuggestion:
			if matches := reSeverity.FindStringSubmatch(trimmed); len(matches) >= 2 {
				currentSuggestion.Severity = strings.TrimSpace(matches[1])
			} else if matches := reCategory.FindStringSubmatch(trimmed); len(matches) >= 2 {
				currentSuggestion.Category = strings.TrimSpace(matches[1])
			}
		case StateInComment:
			commentBuilder.WriteString(line + "\n")
		}
	}

	// Final cleanup
	if currentSuggestion != nil && state == StateInComment {
		currentSuggestion.Comment = strings.TrimSpace(commentBuilder.String())
		review.Suggestions = append(review.Suggestions, *currentSuggestion)
	}

	review.Summary = strings.TrimSpace(review.Summary)
	review.Verdict = strings.TrimSpace(review.Verdict)

	// Fallback if verdict is missing but summary exists? No, enforce strictness for now.
	if review.Verdict == "" && review.Summary == "" {
		return nil, fmt.Errorf("failed to parse review: missing summary and verdict")
	}

	return review, nil
}
