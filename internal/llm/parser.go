package llm

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/sevigo/code-warden/internal/core"
)

var (
	reFixCodeCleanup = regexp.MustCompile(`(?is)<fix_code>.*?</fix_code>`)
	reCodeSugCleanup = regexp.MustCompile(`(?is)<code_suggestion>.*?</code_suggestion>`)
)

// ParseMarkdownReview extracts structured review data from the LLM's XML-tagged output.
// It handles preambles gracefully and maintains a fallback for legacy markdown formats.
func ParseMarkdownReview(ctx context.Context, markdown string, logger *slog.Logger) (*core.StructuredReview, error) {
	// 1. Normalize line endings
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")

	// 2. Try XML first (Preferred Protocol)
	if review, ok := parseXMLReview(ctx, markdown, logger); ok {
		return review, nil
	}

	// 3. Fallback to Legacy Markdown Parser if no <review> tags are found
	return parseLegacyMarkdownReview(markdown)
}

type innerXMLString struct {
	Content string `xml:",innerxml"`
}

type xmlReview struct {
	Verdict     string          `xml:"verdict"`
	Confidence  int             `xml:"confidence"`
	Summary     innerXMLString  `xml:"summary"`
	Suggestions []xmlSuggestion `xml:"suggestions>suggestion"`
}

type xmlSuggestion struct {
	File             string         `xml:"file"`
	Line             string         `xml:"line"`
	Severity         string         `xml:"severity"`
	Category         string         `xml:"category"`
	Confidence       int            `xml:"confidence"`
	EstimatedFixTime string         `xml:"estimated_fix_time"`
	Reproducibility  string         `xml:"reproducibility"`
	Comment          innerXMLString `xml:"comment"`
	CodeSuggestion   innerXMLString `xml:"code_suggestion"`
	FixCode          innerXMLString `xml:"fix_code"` // Legacy syntax fallback
}

// decodeXMLReview handles the raw XML extraction and structured decoding
func decodeXMLReview(ctx context.Context, markdown string, logger *slog.Logger) (*xmlReview, bool) {
	// Pre-process markdown to fix common LLM XML hallucinations
	markdown = strings.ReplaceAll(markdown, "</ ", "</")

	// If it looks like it's missing closing tags due to truncation, append them
	if strings.Contains(markdown, "<review>") && !strings.Contains(markdown, "</review>") {
		markdown += "</summary></review>"
	}

	reader := strings.NewReader(markdown)
	decoder := xml.NewDecoder(reader)
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose

	var xr xmlReview
	for {
		// Respect context cancellation
		select {
		case <-ctx.Done():
			return nil, false
		default:
		}

		t, err := decoder.Token()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				logger.Warn("XML token decoding error", "error", err)
			}
			return nil, false
		}
		if se, ok := t.(xml.StartElement); ok && strings.EqualFold(se.Name.Local, "review") {
			// Found <review>, now decode into xr
			if err := decoder.DecodeElement(&xr, &se); err != nil {
				logger.Warn("XML decoding failed", "error", err)
				return nil, false
			}
			return &xr, true
		}
	}
}

// parseXMLReview implements the core XML-tagged parsing logic.
func parseXMLReview(ctx context.Context, markdown string, logger *slog.Logger) (*core.StructuredReview, bool) {
	xr, ok := decodeXMLReview(ctx, markdown, logger)
	if !ok {
		return nil, false
	}

	review := &core.StructuredReview{
		Verdict:    normalizeVerdict(xr.Verdict),
		Confidence: xr.Confidence,
		Summary:    unindent(xr.Summary.Content),
	}

	for _, xs := range xr.Suggestions {
		if ctx.Err() != nil {
			return nil, false
		}

		suggestion := parseXMLSuggestion(&xs, logger)
		if suggestion != nil {
			review.Suggestions = append(review.Suggestions, *suggestion)
		}
	}

	// If we found the tag but nothing useful was inside, it might be a hallucination
	if review.Summary == "" && len(review.Suggestions) == 0 && review.Verdict == "" {
		return nil, false
	}

	return review, true
}

// parseXMLSuggestion converts an unmarshaled xmlSuggestion back into the domain core.Suggestion struct
func parseXMLSuggestion(xs *xmlSuggestion, logger *slog.Logger) *core.Suggestion {
	if xs.File == "" || xs.Line == "" {
		return nil
	}

	s := &core.Suggestion{
		FilePath: sanitizePath(xs.File),
	}
	if s.FilePath == "" {
		return nil
	}

	lineStr := xs.Line
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

	s.Severity = strings.TrimSpace(xs.Severity)
	s.Category = strings.TrimSpace(xs.Category)
	s.Confidence = xs.Confidence
	s.EstimatedFixTime = strings.TrimSpace(xs.EstimatedFixTime)
	s.Reproducibility = strings.TrimSpace(xs.Reproducibility)

	if xs.Comment.Content != "" {
		// Clean up the comment by removing any embedded <fix_code> or <code_suggestion> tags
		// ensuring they don't leak into the visible GitHub comment body.
		// Because xs.Comment is an innerXML field, it retains the raw tags.
		// Let's strip the known suggestion blocks from the comment entirely using regex
		cleaned := xs.Comment.Content

		cleaned = reFixCodeCleanup.ReplaceAllString(cleaned, "")
		cleaned = reCodeSugCleanup.ReplaceAllString(cleaned, "")
		s.Comment = unindent(cleaned)
	}

	if xs.CodeSuggestion.Content != "" {
		const maxCodeBytes = 10_000
		if len(xs.CodeSuggestion.Content) > maxCodeBytes {
			logger.Warn("code suggestion exceeds safe size", "size", len(xs.CodeSuggestion.Content))
		}
		s.CodeSuggestion = stripMarkdownFence(unindent(xs.CodeSuggestion.Content))
	} else if xs.FixCode.Content != "" {
		logger.Warn("using deprecated <fix_code> tag")
		s.CodeSuggestion = stripMarkdownFence(unindent(xs.FixCode.Content))
	}

	return s
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

// sanitizePath ensures the file path is safe and relative.
func sanitizePath(path string) string {
	if path == "" {
		return ""
	}

	// Strip common LLM artifacts
	path = strings.ReplaceAll(path, "*", "")
	path = strings.ReplaceAll(path, "`", "")
	path = strings.ReplaceAll(path, "\"", "")
	path = strings.ReplaceAll(path, "'", "")
	path = strings.TrimSpace(path)

	// Reject traversal attempts *before* cleaning
	if strings.Contains(path, "..") || strings.Contains(path, "//") || strings.Contains(path, "\\\\") {
		return ""
	}

	// Normalize separators to forward slashes for uniform handling
	path = strings.ReplaceAll(path, "\\", "/")

	// Reject absolute paths and Windows drive letters
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `.\`) ||
		(len(path) > 1 && path[1] == ':') {
		return ""
	}

	// Final validation after cleaning
	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, `\`) ||
		strings.HasPrefix(cleaned, `.\`) || strings.HasPrefix(cleaned, "..") ||
		strings.Contains(cleaned, "/..") || strings.Contains(cleaned, `\..`) {
		return ""
	}

	return filepath.ToSlash(cleaned)
}

// normalizeVerdict maps a string to canonical core.Verdict constants.
func normalizeVerdict(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	// Remove brackets if present
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")

	switch v {
	case "APPROVE", "APPROVED":
		return core.VerdictApprove
	case "REQUEST_CHANGES", "CHANGES_REQUESTED":
		return core.VerdictRequestChanges
	case "COMMENT", "NEEDS_DISCUSSION":
		return core.VerdictComment
	default:
		return core.VerdictComment
	}
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

	// Enforce hard limits to prevent ReDoS
	const maxLines = 10000
	if len(lines) > maxLines {
		return ""
	}

	fenceDepth := 0
	startLine := -1
	endLine := -1

	for i := range lines {
		line := strings.TrimSpace(lines[i])

		// Only process lines that are pure fence markers (```)
		if !strings.HasPrefix(line, "```") {
			continue
		}

		// Count backtick sequences — only match standalone fences
		if strings.Count(line, "```") != 1 {
			continue // Skip lines with multiple ``` sequences
		}

		if fenceDepth == 0 {
			// Opening fence
			fenceDepth = 1
			startLine = i
		} else {
			// Closing fence
			endLine = i
			break
		}

		if fenceDepth > 5 {
			return "" // reject deeply nested fences
		}
	}

	// Case 1: Found both opening and closing fence
	if startLine != -1 && endLine != -1 && endLine > startLine+1 {
		return strings.Join(lines[startLine+1:endLine], "\n")
	}

	// Case 2: Found opening fence but no closing (unclosed fence - be lenient)
	if startLine != -1 && endLine == -1 && len(lines) > startLine+1 {
		return strings.Join(lines[startLine+1:], "\n")
	}

	// Case 3: Malformed or empty content
	return ""
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
