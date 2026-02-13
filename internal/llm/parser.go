package llm

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// parseMarkdownReview extracts structured review data from the LLM's XML-tagged output.
// It is "Preamble-Resilient" and handles rich Markdown inside tags.
func parseMarkdownReview(markdown string) (*core.StructuredReview, error) {
	// 1. Normalize line endings
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")

	// 2. Find the root <review> tag
	reviewContent, ok := extractTag(markdown, "review")
	if !ok {
		// Fallback: If no tags are found, we might be receiving legacy Markdown.
		// However, the requirement is to use XML, so we should ideally fail or
		// implement a very minimal best-effort.
		// For now, let's strictly require the <review> tag for the new protocol.
		return nil, fmt.Errorf("failed to parse review: <review> tag not found")
	}

	review := &core.StructuredReview{}

	// 3. Extract Verdict
	if v, ok := extractTag(reviewContent, "verdict"); ok {
		review.Verdict = normalizeVerdict(v)
	}

	// 4. Extract Review Confidence
	if c, ok := extractTag(reviewContent, "confidence"); ok {
		review.Confidence, _ = strconv.Atoi(strings.TrimSpace(c))
	}

	// 5. Extract Summary
	if s, ok := extractTag(reviewContent, "summary"); ok {
		review.Summary = strings.TrimSpace(s)
	}

	// 5. Extract Suggestions
	suggestionsContent, _ := extractTag(reviewContent, "suggestions")
	// If <suggestions> is missing but there are <suggestion> tags in reviewContent,
	// we handle those too for extra resilience.
	sourceForSuggestions := suggestionsContent
	if sourceForSuggestions == "" {
		sourceForSuggestions = reviewContent
	}

	suggestionBlocks := extractMultipleTags(sourceForSuggestions, "suggestion")
	for _, block := range suggestionBlocks {
		s := parseSuggestionBlock(block)
		if s != nil {
			review.Suggestions = append(review.Suggestions, *s)
		}
	}

	// Validation
	if review.Summary == "" && len(review.Suggestions) == 0 && review.Verdict == "" {
		return nil, fmt.Errorf("failed to parse review: no recognized content inside <review> tags")
	}

	return review, nil
}

// parseSuggestionBlock extracts fields from a single <suggestion> block.
func parseSuggestionBlock(content string) *core.Suggestion {
	file, fileOk := extractTag(content, "file")
	lineStr, lineOk := extractTag(content, "line")
	if !fileOk || !lineOk {
		return nil
	}

	s := &core.Suggestion{
		FilePath: sanitizePath(file),
	}

	// Handle single line or range (10-20)
	if strings.Contains(lineStr, "-") {
		parts := strings.Split(lineStr, "-")
		if len(parts) >= 2 {
			s.StartLine, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
			s.LineNumber, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
		}
	} else {
		line, _ := strconv.Atoi(strings.TrimSpace(lineStr))
		s.LineNumber = line
		s.StartLine = line
	}

	if s.LineNumber <= 0 {
		return nil
	}

	if sev, ok := extractTag(content, "severity"); ok {
		s.Severity = strings.TrimSpace(sev)
	}
	if cat, ok := extractTag(content, "category"); ok {
		s.Category = strings.TrimSpace(cat)
	}
	if comm, ok := extractTag(content, "comment"); ok {
		s.Comment = strings.TrimSpace(comm)
	}
	if conf, ok := extractTag(content, "confidence"); ok {
		s.Confidence, _ = strconv.Atoi(strings.TrimSpace(conf))
	}
	if eft, ok := extractTag(content, "estimated_fix_time"); ok {
		s.EstimatedFixTime = strings.TrimSpace(eft)
	}
	if repro, ok := extractTag(content, "reproducibility"); ok {
		s.Reproducibility = strings.TrimSpace(repro)
	}

	return s
}

// extractTag finds the content between <tag> and </tag>.
func extractTag(content, tag string) (string, bool) {
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"

	startIdx := strings.Index(content, startTag)
	if startIdx == -1 {
		return "", false
	}

	endIdx := strings.Index(content[startIdx:], endTag)
	if endIdx == -1 {
		return "", false
	}

	return content[startIdx+len(startTag) : startIdx+endIdx], true
}

// extractMultipleTags finds all occurrences of content between <tag> and </tag>.
func extractMultipleTags(content, tag string) []string {
	var results []string
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"

	curr := content
	for {
		startIdx := strings.Index(curr, startTag)
		if startIdx == -1 {
			break
		}
		endIdx := strings.Index(curr[startIdx:], endTag)
		if endIdx == -1 {
			break
		}

		results = append(results, curr[startIdx+len(startTag):startIdx+endIdx])
		curr = curr[startIdx+endIdx+len(endTag):]
	}
	return results
}

// sanitizePath aggressively strips common LLM formatting from file paths.
func sanitizePath(path string) string {
	path = strings.TrimSpace(path)
	// Remove markdown markers often hallucinanted inside tags
	path = strings.ReplaceAll(path, "*", "")
	path = strings.ReplaceAll(path, "`", "")
	path = strings.ReplaceAll(path, "\"", "")
	path = strings.ReplaceAll(path, "'", "")
	return strings.TrimSpace(path)
}

// normalizeVerdict maps a string to canonical core.Verdict constants.
func normalizeVerdict(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, " ", "_")
	v = strings.Trim(v, "[]")

	switch {
	case strings.Contains(v, "APPROVE"):
		return core.VerdictApprove
	case strings.Contains(v, "REQUEST_CHANGES"):
		return core.VerdictRequestChanges
	case strings.Contains(v, "COMMENT"):
		return core.VerdictComment
	default:
		return ""
	}
}

// stripMarkdownFence is kept for a first-pass cleanup if the LLM wraps the whole XML in a code block.
func stripMarkdownFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return s
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return s
	}

	// Find first closing fence
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			closeIdx = i
			break
		}
	}

	if closeIdx > 0 {
		return strings.TrimSpace(strings.Join(lines[1:closeIdx], "\n"))
	}
	return strings.TrimSpace(strings.Join(lines[1:], "\n"))
}
