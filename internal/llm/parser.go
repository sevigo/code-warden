package llm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/sevigo/code-warden/internal/core"
)

var (
	tagRegexCache = make(map[string]*regexp.Regexp)
	cacheMu       sync.RWMutex
	pathReplacer  = strings.NewReplacer("*", "", "`", "", "\"", "", "'", "")
)

// getTagRegex returns a pre-compiled regex for the given XML tag.
// It uses a memoization cache to avoid repeated compilation overhead.
func getTagRegex(tag string) *regexp.Regexp {
	quotedTag := regexp.QuoteMeta(tag)
	cacheMu.RLock()
	re, ok := tagRegexCache[quotedTag] // Use quoted tag as key to prevent collisions
	cacheMu.RUnlock()
	if ok {
		return re
	}

	cacheMu.Lock()
	defer cacheMu.Unlock()
	// Double-check after acquiring write lock
	if re, ok = tagRegexCache[quotedTag]; ok {
		return re
	}
	re = regexp.MustCompile(`(?is)<` + quotedTag + `\b[^>]*>(.*?)</` + quotedTag + `\s*>`)
	tagRegexCache[quotedTag] = re
	return re
}

// ParseMarkdownReview extracts structured review data from the LLM's XML-tagged output.
// It handles preambles gracefully and maintains a fallback for legacy markdown formats.
func ParseMarkdownReview(markdown string) (*core.StructuredReview, error) {
	// 1. Normalize line endings
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")

	// 2. Try XML first (Preferred Protocol)
	if review, ok := parseXMLReview(markdown); ok {
		return review, nil
	}

	// 3. Fallback to Legacy Markdown Parser if no <review> tags are found
	return parseLegacyMarkdownReview(markdown)
}

// parseXMLReview implements the core XML-tagged parsing logic.
func parseXMLReview(markdown string) (*core.StructuredReview, bool) {
	reviewContent, ok := extractTag(markdown, "review")
	if !ok {
		return nil, false
	}

	review := &core.StructuredReview{}

	// Extract Verdict
	if v, ok := extractTag(reviewContent, "verdict"); ok {
		review.Verdict = normalizeVerdict(v)
	}

	// Extract Confidence
	if c, ok := extractTag(reviewContent, "confidence"); ok {
		review.Confidence = parseInt(c)
	}

	// Extract Summary
	if s, ok := extractTag(reviewContent, "summary"); ok {
		review.Summary = unindent(s)
	}

	// Extract Suggestions
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

	// If we found the tag but nothing useful was inside, it might be a hallucination
	if review.Summary == "" && len(review.Suggestions) == 0 && review.Verdict == "" {
		return nil, false
	}

	return review, true
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
	if s.FilePath == "" {
		return nil
	}

	// Normalize typographic dashes (En/Em) before splitting
	lineStr = strings.ReplaceAll(lineStr, "–", "-") // En dash
	lineStr = strings.ReplaceAll(lineStr, "—", "-") // Em dash

	// Handle single line or range (10-20)
	if strings.Contains(lineStr, "-") {
		parts := strings.Split(lineStr, "-")
		if len(parts) >= 2 {
			s.StartLine = parseInt(parts[0])
			s.LineNumber = parseInt(parts[1])
		}
	} else {
		line := parseInt(lineStr)
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
		s.Confidence = parseInt(conf)
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
	re := getTagRegex(tag)
	match := re.FindStringSubmatch(content)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// extractMultipleTags finds all occurrences of content between <tag> and </tag>.
func extractMultipleTags(content, tag string) []string {
	results := []string{}
	re := getTagRegex(tag)
	matches := re.FindAllStringSubmatch(content, -1)
	for _, m := range matches {
		results = append(results, m[1])
	}
	return results
}

// parseInt safely converts string to int, returning 0 on error.
// It is robust against non-digit noise like percentages or brackets.
func parseInt(s string) int {
	// 1. Remove non-digit noise (handle things like "95%" or "[95]")
	s = strings.TrimFunc(s, func(r rune) bool {
		return !unicode.IsDigit(r)
	})
	// 2. Convert to int
	v, _ := strconv.Atoi(s)
	return v
}

// unindent removes common leading whitespace from multiline strings.
func unindent(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= 1 {
		return strings.TrimSpace(s)
	}

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

// sanitizePath strips LLM-specific formatting from file paths
func sanitizePath(path string) string {
	path = strings.TrimSpace(path)
	path = pathReplacer.Replace(path)

	// Normalize backslashes to forward slashes for consistent checking
	normalized := strings.ReplaceAll(path, "\\", "/")

	// Prevent absolute paths (Unix and Windows-style) and traversal
	if strings.HasPrefix(normalized, "/") ||
		(len(normalized) > 1 && normalized[1] == ':') || // Windows drive C:
		strings.Contains(normalized, "..") ||
		strings.Contains(normalized, "//") {
		return ""
	}
	return strings.TrimSpace(path)
}

// normalizeVerdict maps a string to canonical core.Verdict constants.
func normalizeVerdict(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, " ", "_")
	v = strings.Trim(v, "[]")

	switch v {
	case core.VerdictApprove:
		return core.VerdictApprove
	case core.VerdictRequestChanges:
		return core.VerdictRequestChanges
	case core.VerdictComment:
		return core.VerdictComment
	default:
		return ""
	}
}

// parseLegacyMarkdownReview handles older formats without XML tags.
func parseLegacyMarkdownReview(markdown string) (*core.StructuredReview, error) {
	markdown = stripMarkdownFence(markdown)
	lines := strings.Split(markdown, "\n")
	review := &core.StructuredReview{}

	currentSection := ""
	var currentSuggestion *core.Suggestion
	var commentBuilder strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)

		// Section detection
		if section := detectLegacySection(upper); section != "" {
			currentSection = section
			continue
		}

		// Suggestion detection (Legacy Header)
		if filePath, start, end, ok := parseLegacySuggestionHeader(trimmed); ok {
			if currentSuggestion != nil {
				currentSuggestion.Comment = strings.TrimSpace(commentBuilder.String())
				review.Suggestions = append(review.Suggestions, *currentSuggestion)
			}
			currentSuggestion = &core.Suggestion{
				FilePath:   filePath,
				StartLine:  start,
				LineNumber: end,
			}
			commentBuilder.Reset()
			continue
		}

		accumulateLegacyContent(line, currentSection, &review.Summary, currentSuggestion, &commentBuilder)
	}

	// Final flush
	if currentSuggestion != nil {
		currentSuggestion.Comment = strings.TrimSpace(commentBuilder.String())
		review.Suggestions = append(review.Suggestions, *currentSuggestion)
	}
	review.Summary = strings.TrimSpace(review.Summary)

	if review.Summary == "" && len(review.Suggestions) == 0 && review.Verdict == "" {
		return nil, fmt.Errorf("failed to parse review: no XML found and legacy parsing yielded no results")
	}

	return review, nil
}

func detectLegacySection(upper string) string {
	switch {
	case strings.Contains(upper, "# VERDICT"):
		return "verdict"
	case strings.Contains(upper, "# SUMMARY") || strings.Contains(upper, "# REVIEW SUMMARY"):
		return "summary"
	case strings.Contains(upper, "# SUGGESTIONS"):
		return "suggestions"
	default:
		return ""
	}
}

func accumulateLegacyContent(line, section string, summary *string, suggestion *core.Suggestion, commentBuilder *strings.Builder) {
	trimmed := strings.TrimSpace(line)
	upper := strings.ToUpper(trimmed)

	switch section {
	case "verdict":
		// Handled directly if needed, but usually summary contains it in legacy
	case "summary":
		*summary += line + "\n"
	case "suggestions":
		if suggestion != nil {
			switch {
			case strings.HasPrefix(upper, "**SEVERITY:**"):
				suggestion.Severity = strings.TrimSpace(trimmed[len("**SEVERITY:**"):])
			case strings.HasPrefix(upper, "**CATEGORY:**"):
				suggestion.Category = strings.TrimSpace(trimmed[len("**CATEGORY:**"):])
			default:
				commentBuilder.WriteString(line + "\n")
			}
		}
	}
}

// parseLegacySuggestionHeader safely parses "File:123" or "Suggestion [file.go]:45-60" without regex.
// Safe for untrusted LLM output; no backtracking possible.
func parseLegacySuggestionHeader(line string) (string, int, int, bool) {
	// Trim leading markdown artifacts and whitespace
	cleaned := strings.TrimFunc(line, func(r rune) bool {
		return unicode.IsSpace(r) || r == '*' || r == '`' || r == '[' || r == ']'
	})

	// Detect typical header prefixes and strip them
	prefixes := []string{"Suggestion", "File", "FILE", "suggestion"}
	for _, p := range prefixes {
		if strings.HasPrefix(cleaned, p) {
			cleaned = strings.TrimSpace(strings.TrimPrefix(cleaned, p))
			break
		}
	}
	cleaned = strings.TrimLeft(cleaned, ": ")

	lastColon := strings.LastIndex(cleaned, ":")
	if lastColon <= 0 {
		return "", 0, 0, false
	}

	path := strings.TrimSpace(cleaned[:lastColon])
	linesPart := strings.TrimSpace(cleaned[lastColon+1:])

	// Normalize various dash variants
	linesPart = strings.ReplaceAll(linesPart, "–", "-")
	linesPart = strings.ReplaceAll(linesPart, "—", "-")

	// Handle ranges (e.g., "12-15")
	if strings.Contains(linesPart, "-") {
		parts := strings.Split(linesPart, "-")
		if len(parts) != 2 {
			return "", 0, 0, false
		}
		startStr := strings.TrimSpace(parts[0])
		endStr := strings.TrimSpace(parts[1])
		start, err1 := strconv.Atoi(startStr)
		end, err2 := strconv.Atoi(endStr)
		if err1 != nil || err2 != nil || start <= 0 || end <= 0 || start > end {
			return "", 0, 0, false
		}
		return path, start, end, true
	}

	// Single line
	ln, err := strconv.Atoi(linesPart)
	if err != nil || ln <= 0 {
		return "", 0, 0, false
	}
	return path, ln, ln, true
}

func stripMarkdownFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return s
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return s
	}

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
