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

const (
	// maxLineLength limits the length of lines we parse to prevent DoS via large allocations.
	maxLineLength = 4096
)

// parseSuggestionHeader extracts file path and line number from a suggestion header.
// Format: "## Suggestion [path/to/file.go:123]"
// Standardizes on manual parsing to be 1. ReDoS-proof (linear time) and 2. Windows-path friendly.
// parseSuggestionHeader parses a line starting with "## Suggestion".
// Returns filePath, startLine, endLine, success.
// If valid, startLine is 0 for single-line suggestions, or >0 for ranges.
func parseSuggestionHeader(line string) (string, int, int, bool) {
	if len(line) > maxLineLength {
		return "", 0, 0, false
	}

	// Clean up the header line
	header := strings.TrimSpace(line)
	header = strings.TrimPrefix(header, "##")
	header = strings.TrimSpace(header)

	// Strategy 1: "Suggestion [path:line]" or "Suggestion path:line"
	// Check case-insensitive prefix
	if strings.HasPrefix(strings.ToLower(header), "suggestion") {
		// Strip "Suggestion"
		rest := header[len("suggestion"):]
		rest = strings.TrimSpace(rest)
		// Strip outer brackets if present
		rest = strings.TrimPrefix(rest, "[")
		rest = strings.TrimSuffix(rest, "]")
		rest = strings.TrimSpace(rest)

		if path, start, end, ok := parsePathAndLine(rest); ok {
			return path, start, end, true
		}
		// If it started with "Suggestion" but failed to parse, it's likely a malformed suggestion header.
		// However, we should double check if "Suggestion" is actually part of the filename?
		// Unlikely. We return false here to avoid false positives in Strategy 2.
		return "", 0, 0, false
	}

	// Strategy 2: Direct "path:line" format (e.g. "internal/storage/database.go:250")
	// Also handle if it's wrapped in brackets like "[path:line]"
	cleanHeader := strings.TrimPrefix(header, "[")
	cleanHeader = strings.TrimSuffix(cleanHeader, "]")

	if path, start, end, ok := parsePathAndLine(cleanHeader); ok {
		return path, start, end, true
	}

	return "", 0, 0, false
}

// parsePathAndLine helper handles "path:line" or "path:start-end" strings
func parsePathAndLine(s string) (string, int, int, bool) {
	lastColon := strings.LastIndex(s, ":")
	if lastColon == -1 {
		return "", 0, 0, false
	}

	pathPart := strings.TrimSpace(s[:lastColon])
	linePart := strings.TrimSpace(s[lastColon+1:])

	// Normalize dashes (En Dash, Em Dash) to standard hyphen
	linePart = strings.ReplaceAll(linePart, "–", "-") // En Dash
	linePart = strings.ReplaceAll(linePart, "—", "-") // Em Dash

	// Validate path is not empty and not just "Suggestion"
	if pathPart == "" || strings.EqualFold(pathPart, "suggestion") {
		return "", 0, 0, false
	}
	// Basic sanitization
	if strings.ContainsAny(pathPart, "\x00\r\n") {
		return "", 0, 0, false
	}

	// Check for range (start-end)
	if strings.Contains(linePart, "-") {
		parts := strings.Split(linePart, "-")
		if len(parts) == 2 {
			start, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 == nil && err2 == nil && start > 0 && end > 0 && start <= end {
				return pathPart, start, end, true
			}
		}
	}

	// Single line: use same value for start and end (non-zero)
	lineNum, err := strconv.Atoi(linePart)
	if err != nil || lineNum <= 0 {
		return "", 0, 0, false
	}

	return pathPart, lineNum, lineNum, true // StartLine must equal LineNumber for single-line consistency
}

// ParseError represents a failure to parse the LLM output into a structured format.
type ParseError struct {
	Message string
	Err     error
}

func (e *ParseError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *ParseError) Unwrap() error {
	return e.Err
}

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

		// Check for suggestion header (generic or specific)
		if filePath, startLine, endLine, ok := parseSuggestionHeader(line); ok {
			// Flush previous suggestion
			flushSuggestion(review, currentSuggestion, &commentBuilder)

			currentSuggestion = &core.Suggestion{
				FilePath:   filePath,
				StartLine:  startLine,
				LineNumber: endLine,
			}
			currentSection = "SUGGESTION_CONTENT"
			continue
		}

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

func processVerdictLine(line string, review *core.StructuredReview, _ *strings.Builder) {
	if line != "" && !strings.HasPrefix(line, "#") {
		review.Verdict = line // Critical: preserve structured field
		// Do not append to Summary here. Status updater will handle formatting.
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

	// Find first closing fence after the opening line (scanning forward)
	// This prevents over-stripping if the LLM output contains multiple fenced blocks or trailing text.
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

	// Fallback: If closing fence is missing (truncation), return everything after the opening fence
	return strings.TrimSpace(strings.Join(lines[1:], "\n"))
}
