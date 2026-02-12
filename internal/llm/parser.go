package llm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

var (
	// Anchored Regex for strict header detection
	// Support both Markdown headers (#) and Blockquote headers (>) for flexibility.
	// We allow optional emojis/symbols before the keywords.
	headerSummary     = regexp.MustCompile(`(?i)^(?:#{1,6}|>)\s*(?:[^a-zA-Z0-9\n\r]*\s*)?(?:CODE\s+WARDEN\s+)?(?:REVIEW\s+SUMMARY|SUMMARY|CONSENSUS\s+REVIEW|EXECUTIVE\s+SUMMARY)\b`)
	headerVerdict     = regexp.MustCompile(`(?i)^(?:#{1,6}|>)\s*(?:[^a-zA-Z0-9\n\r]*\s*)?VERDICT\b`)
	headerSuggestions = regexp.MustCompile(`(?i)^(?:#{1,6}|>)\s*(?:[^a-zA-Z0-9\n\r]*\s*)?(?:DETAILED\s+)?(?:SUGGESTIONS|KEY\s+FINDINGS)\b`)

	// Regex for extracting attributes
	verdictAttribute  = regexp.MustCompile(`(?i)(?:VERDICT[:\s]+)?\[?((?:APPROVE|REQUEST_CHANGES|REQUEST\s+CHANGES|COMMENT))\]?`)
	severityAttribute = regexp.MustCompile(`(?i)\*\*Severity:?\*\*\s*(.*)`)
	categoryAttribute = regexp.MustCompile(`(?i)\*\*Category:?\*\*\s*(.*)`)

	// File path parsing context (New: **File:** path)
	fileAttribute = regexp.MustCompile(`(?i)\*\*File:\*\*\s*(.*)`)
)

const (
	maxLineLength = 4096

	// Parser States
	stateInit           = "INIT"
	stateSummary        = "SUMMARY"
	stateVerdict        = "VERDICT"
	stateSuggestions    = "SUGGESTIONS"
	stateSuggestionBody = "SUGGESTION_BODY"
)

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
// It uses a robust state machine to handle the structure.
func parseMarkdownReview(markdown string) (*core.StructuredReview, error) {
	parser := &reviewParser{
		state:  stateInit,
		review: &core.StructuredReview{},
	}
	return parser.parse(markdown)
}

type reviewParser struct {
	state             string
	review            *core.StructuredReview
	currentSuggestion *core.Suggestion
	commentBuilder    strings.Builder
	summaryBuilder    strings.Builder
}

func (p *reviewParser) parse(markdown string) (*core.StructuredReview, error) {
	// 1. Normalize line endings
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")

	// 2. Strip wrapping markdown code fence
	markdown = stripMarkdownFence(markdown)

	lines := strings.Split(markdown, "\n")

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if len(line) > maxLineLength {
			continue // Skip excessively long lines to prevent DoS
		}
		p.processLine(line, rawLine)
	}

	// Final flush
	if p.summaryBuilder.Len() > 0 {
		if p.review.Summary != "" {
			p.review.Summary += "\n\n" + p.summaryBuilder.String()
		} else {
			p.review.Summary = p.summaryBuilder.String()
		}
	}
	p.flushSuggestion()

	// Validation
	if p.review.Summary == "" && len(p.review.Suggestions) == 0 && p.review.Verdict == "" {
		return nil, fmt.Errorf("failed to parse review: no recognized sections found")
	}

	return p.review, nil
}

func (p *reviewParser) processLine(line, rawLine string) {
	// --- State Transition Logic (Headers) ---
	if p.checkHeaders(line) {
		return
	}

	// --- Suggestion Detection ---
	if filePath, start, end, ok := parseSuggestionHeader(line); ok {
		p.flushSuggestion()
		p.state = stateSuggestionBody
		p.currentSuggestion = &core.Suggestion{
			FilePath:   filePath,
			StartLine:  start,
			LineNumber: end,
		}
		return
	}

	// --- Content Parsing based on State ---
	p.handleContent(line, rawLine)
}

func (p *reviewParser) checkHeaders(line string) bool {
	if headerSummary.MatchString(line) {
		p.flushSuggestion()
		p.state = stateSummary
		return true
	}

	if headerVerdict.MatchString(line) {
		p.flushSuggestion()
		p.state = stateVerdict
		// If verdict is inline, extract immediately
		if v := extractVerdictFromLine(line); v != "" {
			p.review.Verdict = v
		}
		return true
	}

	if headerSuggestions.MatchString(line) {
		p.flushSuggestion()
		p.state = stateSuggestions
		return true
	}
	return false
}

func (p *reviewParser) handleContent(line, rawLine string) {
	switch p.state {
	case stateSummary:
		// Accumulate summary text. Ignore sub-headers if they look like formatting.
		if line != "" && !strings.HasPrefix(line, "#") {
			if p.summaryBuilder.Len() > 0 {
				p.summaryBuilder.WriteString("\n")
			}
			p.summaryBuilder.WriteString(line)
		}

	case stateVerdict:
		if p.review.Verdict == "" {
			if v := extractVerdictFromLine(line); v != "" {
				p.review.Verdict = v
			} else {
				p.review.Verdict = extractVerdictFallback(line)
			}
		}

	case stateSuggestions:
		// Accumulate the Title before we even know the file path
		// We capture ANY header (### or ####) as a potential title line for the NEXT suggestion.
		if strings.HasPrefix(line, "###") || strings.HasPrefix(line, "####") {
			// Ensure we have a separator if needed, but per-spec:
			p.commentBuilder.WriteString("\n" + rawLine + "\n")
		}

	case stateSuggestionBody:
		if p.currentSuggestion == nil {
			return
		}
		processSuggestionLine(line, rawLine, p.currentSuggestion, &p.commentBuilder)
	}
}

func (p *reviewParser) flushSuggestion() {
	if p.currentSuggestion != nil {
		if p.commentBuilder.Len() > 0 {
			p.currentSuggestion.Comment = strings.TrimSpace(p.commentBuilder.String())
			p.commentBuilder.Reset()
		}
		p.review.Suggestions = append(p.review.Suggestions, *p.currentSuggestion)
		p.currentSuggestion = nil
	}
}

// extractVerdictFromLine attempts to find a verdict string in a line
func extractVerdictFromLine(line string) string {
	matches := verdictAttribute.FindStringSubmatch(line)
	if len(matches) > 1 {
		v := strings.TrimSpace(matches[1])
		// Normalize spaces to underscores for comparison with constants
		v = strings.ReplaceAll(v, " ", "_")
		v = strings.ToUpper(v)
		if v == core.VerdictRequestChanges || v == core.VerdictApprove || v == core.VerdictComment {
			return v
		}
	}
	return ""
}

// extractVerdictFallback checks for bare keywords if strict format failed
func extractVerdictFallback(line string) string {
	upper := strings.ToUpper(line)
	if strings.Contains(upper, core.VerdictApprove) {
		return core.VerdictApprove
	}
	if strings.Contains(upper, core.VerdictRequestChanges) || strings.Contains(upper, "REQUEST CHANGES") {
		return core.VerdictRequestChanges
	}
	if strings.Contains(upper, core.VerdictComment) {
		return core.VerdictComment
	}
	return ""
}

// 2. ## Suggestion [path/to/file.go:123] (Traditional)
// 3. [path/to/file.go:123] (Generic)
func parseSuggestionHeader(line string) (string, int, int, bool) {
	if len(line) > maxLineLength {
		return "", 0, 0, false
	}
	// Strategy 1: **File:**
	if matches := fileAttribute.FindStringSubmatch(line); len(matches) > 1 {
		content := strings.TrimSpace(matches[1])
		// CRITICAL: Aggressively strip markdown formatting
		content = strings.ReplaceAll(content, "*", "")
		content = strings.Trim(content, "`\"' ")
		return parsePathAndLine(content)
	}

	// Strategy 2: Flexible Header Level (Traditional ## but allow ###, #### etc)
	if strings.HasPrefix(line, "#") {
		// Check for "suggestion" case-insensitive
		lower := strings.ToLower(line)
		if strings.Contains(lower, "suggestion") {
			// Strip all leading # and whitespace
			cleaned := strings.TrimLeft(line, "# \t")

			// Remove "Suggestion" keyword (case-insensitive)
			// prefix check since we trimmed left
			if len(cleaned) >= 10 && strings.EqualFold(cleaned[:10], "suggestion") {
				cleaned = cleaned[10:]
			}

			cleaned = strings.TrimSpace(cleaned)
			cleaned = strings.TrimPrefix(cleaned, "[")
			cleaned = strings.TrimSuffix(cleaned, "]")
			cleaned = strings.Trim(cleaned, "`")
			return parsePathAndLine(cleaned)
		}
	}

	// Strategy 3: Loose bracket match [path:line] at start of line
	if strings.HasPrefix(line, "[") && strings.Contains(line, ":") && strings.HasSuffix(line, "]") {
		cleaned := strings.Trim(line, "[]` ")
		return parsePathAndLine(cleaned)
	}

	return "", 0, 0, false
}

// parsePathAndLine helper handles "path:line" or "path:start-end" strings
func parsePathAndLine(s string) (string, int, int, bool) {
	// 1. Remove all possible Markdown junk first
	s = strings.ReplaceAll(s, "*", "")
	// Aggressively remove backticks and quotes from ANYWHERE in the string
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.TrimSpace(s)

	lastColon := strings.LastIndex(s, ":")
	if lastColon == -1 {
		return "", 0, 0, false
	}

	// Trimming parts individually to preserve valid path characters usually
	pathPart := strings.TrimSpace(s[:lastColon])
	linePart := strings.TrimSpace(s[lastColon+1:])

	// Basic validation
	if pathPart == "" || strings.EqualFold(pathPart, "suggestion") || strings.ContainsAny(pathPart, "\x00\r\n") {
		return "", 0, 0, false
	}

	// Normalize dashes early — BEFORE splitting on "-"
	linePart = strings.ReplaceAll(linePart, "–", "-") // En Dash
	linePart = strings.ReplaceAll(linePart, "—", "-") // Em Dash

	// Range handling (10-20)
	if strings.Contains(linePart, "-") {
		parts := strings.Split(linePart, "-")
		if len(parts) >= 2 {
			start, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			end, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
			if start > 0 && end >= start {
				return pathPart, start, end, true
			}
		}
	}

	// Single line
	lineNum, err := strconv.Atoi(linePart)
	if err == nil && lineNum > 0 {
		return pathPart, lineNum, lineNum, true
	}

	return "", 0, 0, false
}

func processSuggestionLine(line, rawLine string, suggestion *core.Suggestion, commentBuilder *strings.Builder) {
	// Parse attributes (Severity, Category) or content
	if matches := severityAttribute.FindStringSubmatch(line); len(matches) > 1 {
		suggestion.Severity = strings.TrimSpace(matches[1])
		return
	}
	if matches := categoryAttribute.FindStringSubmatch(line); len(matches) > 1 {
		suggestion.Category = strings.TrimSpace(matches[1])
		return
	}
	if strings.HasPrefix(line, "### Comment") {
		// Skip comment header, content follows
		return
	}
	if strings.HasPrefix(line, "### Rationale") {
		commentBuilder.WriteString("\n\n**Rationale:**\n")
		return
	}
	if strings.HasPrefix(line, "### Fix") {
		commentBuilder.WriteString("\n\n**Fix:**\n")
		return
	}

	// Regular content line
	if commentBuilder.Len() > 0 || line != "" {
		commentBuilder.WriteString(rawLine + "\n")
	}
}

// stripMarkdownFence removes ```markdown ... ``` wrapping
func stripMarkdownFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return s
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return s
	}
	// Check header of fence
	first := strings.ToLower(strings.TrimSpace(lines[0]))
	if !strings.HasPrefix(first, "```") {
		return s
	}
	// If lang specified, usually safe to strip. If no lang, strip.
	lang := strings.TrimPrefix(first, "```")
	if lang != "" && lang != "markdown" && lang != "md" && lang != "text" {
		return s
	}

	// Find first closing fence after the opening line (scanning forward)
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			closeIdx = i
			break
		}
	}

	if closeIdx > 0 {
		// Normalize: trim each line before joining, then trim overall
		return strings.TrimSpace(strings.Join(lines[1:closeIdx], "\n"))
	}
	// Fallback: if closing fence is missing, preserve content *and* apply final trim
	return strings.TrimSpace(strings.Join(lines[1:], "\n"))
}
