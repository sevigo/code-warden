package llm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/sevigo/code-warden/internal/core"
)

var (
	// regex to find <tag>content</tag>, handles potential whitespace in tags like <tag >
	tagRegex = regexp.MustCompile(`(?s)<([\w\d]+)\b[^>]*>(.*?)</\1>`)
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
		review.Summary = unindent(s)
	}

	// 6. Extract Suggestions
	suggestionsContent, _ := extractTag(reviewContent, "suggestions")
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

	// Normalize typographic dashes (En/Em) before splitting
	lineStr = strings.ReplaceAll(lineStr, "–", "-") // En dash
	lineStr = strings.ReplaceAll(lineStr, "—", "-") // Em dash

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
		s.Comment = unindent(comm)
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
	re := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>(.*?)</` + tag + `>`)
	match := re.FindStringSubmatch(content)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// extractMultipleTags finds all occurrences of content between <tag> and </tag>.
func extractMultipleTags(content, tag string) []string {
	results := []string{}
	re := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>(.*?)</` + tag + `>`)
	matches := re.FindAllStringSubmatch(content, -1)
	for _, m := range matches {
		results = append(results, m[1])
	}
	return results
}

// unindent removes common leading whitespace from multiline strings.
// This prevents "pretty-printed" XML from breaking Markdown rendering on GitHub.
func unindent(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= 1 {
		return strings.TrimSpace(s)
	}

	// Find the minimum indentation level (ignoring empty lines)
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := 0
		for _, r := range line {
			if unicode.IsSpace(r) {
				indent++
			} else {
				break
			}
		}
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}

	if minIndent <= 0 {
		return strings.TrimSpace(s)
	}

	var result []string
	for _, line := range lines {
		if len(line) >= minIndent {
			result = append(result, line[minIndent:])
		} else {
			result = append(result, "")
		}
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
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
