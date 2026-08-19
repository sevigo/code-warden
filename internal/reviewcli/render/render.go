package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// Options controls how a review is rendered.
type Options struct {
	// Width is the terminal width for wrapping. 0 disables wrapping.
	Width int
}

// Render writes a structured review to w in a human-friendly, colorized format.
func Render(w io.Writer, review *core.StructuredReview, opts Options) {
	width := opts.Width
	if width <= 0 {
		width = 120
	}

	// Header
	fmt.Fprintln(w, Bold(Cyan("code-warden review")))
	fmt.Fprintln(w)

	// Verdict badge
	verdict := strings.ToUpper(review.Verdict)
	if verdict == "" {
		verdict = "N/A"
	}
	fmt.Fprintln(w, renderVerdict(verdict))

	// Confidence + summary on the same line if both present
	if review.Confidence > 0 {
		fmt.Fprintln(w, Dim(fmt.Sprintf("  confidence: %d%%", review.Confidence)))
	}
	fmt.Fprintln(w)

	if review.Summary != "" {
		fmt.Fprintln(w, Wrap(review.Summary, width))
		fmt.Fprintln(w)
	}

	// Findings
	if len(review.Suggestions) == 0 {
		fmt.Fprintln(w, Green("No findings. ✓"))
		fmt.Fprintln(w)
		return
	}

	// Sort findings by severity (Critical > High > Medium > Low), then file, then line.
	findings := make([]core.Suggestion, len(review.Suggestions))
	copy(findings, review.Suggestions)
	sort.SliceStable(findings, func(i, j int) bool {
		if severityRank(findings[i].Severity) != severityRank(findings[j].Severity) {
			return severityRank(findings[i].Severity) > severityRank(findings[j].Severity)
		}
		if findings[i].FilePath != findings[j].FilePath {
			return findings[i].FilePath < findings[j].FilePath
		}
		return findings[i].LineNumber < findings[j].LineNumber
	})

	for i, f := range findings {
		if i > 0 {
			fmt.Fprintln(w)
		}
		renderFinding(w, f, width)
	}

	// Footer
	fmt.Fprintln(w)
	fmt.Fprintln(w, Dim(fmt.Sprintf("%d finding(s)", len(findings))))
}

// renderVerdict returns a colorized verdict line.
func renderVerdict(verdict string) string {
	switch verdict {
	case "APPROVE":
		return GreenBold("✓ APPROVE")
	case "REQUEST_CHANGES":
		return RedBold("✗ REQUEST CHANGES")
	case "COMMENT":
		return BlueBold("💬 COMMENT")
	default:
		return Bold(verdict)
	}
}

// severityRank maps a severity label to a numeric rank for sorting.
func severityRank(sev string) int {
	switch strings.ToLower(sev) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// severityStyle returns the ANSI style function for a severity label.
func severityStyle(sev string) func(string) string {
	switch strings.ToLower(sev) {
	case "critical":
		return RedBold
	case "high":
		return YellowBold
	case "medium":
		return BlueBold
	case "low":
		return GreenBold
	default:
		return Bold
	}
}

// renderFinding writes a single finding as a structured block.
func renderFinding(w io.Writer, f core.Suggestion, width int) {
	sev := strings.ToUpper(f.Severity)
	if sev == "" {
		sev = "NOTE"
	}
	style := severityStyle(f.Severity)

	// Header: severity pill + file:line
	loc := f.FilePath
	if f.LineNumber > 0 {
		loc = fmt.Sprintf("%s:%d", f.FilePath, f.LineNumber)
	}
	fmt.Fprintf(w, "%s %s\n", style("["+sev+"]"), Bold(Cyan(loc)))

	// Category + source on a dim line
	meta := f.Category
	if f.Source != "" {
		if meta != "" {
			meta += " · "
		}
		meta += f.Source
	}
	if meta != "" {
		fmt.Fprintln(w, Dim("  "+meta))
	}

	// Comment — parse structured sections (Observation, Rationale, Fix)
	// and render each with a label prefix. Code fences get indented.
	if f.Comment != "" {
		fmt.Fprintln(w)
		renderComment(w, f.Comment, width)
	}

	// Code suggestion block
	if f.CodeSuggestion != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  "+GreenBold("Suggested fix:"))
		for _, line := range strings.Split(f.CodeSuggestion, "\n") {
			fmt.Fprintln(w, "    "+Green(line))
		}
	}
}

// renderComment parses the comment for structured sections (**Observation:**,
// **Rationale:**, **Fix:**) and code fences, rendering each with visual
// separation. Falls back to plain wrapping when no structure is found.
func renderComment(w io.Writer, comment string, width int) {
	sections := parseCommentSections(comment)
	if len(sections) == 0 {
		// No structured sections — render as plain text.
		fmt.Fprintln(w, Wrap(indent(comment, "  "), width-2))
		return
	}
	for _, s := range sections {
		switch s.kind {
		case sectionText:
			fmt.Fprintln(w, Wrap(indent(s.text, "  "), width-2))
		case sectionLabel:
			label := Bold(s.label)
			body := strings.TrimSpace(s.text)
			if body == "" {
				fmt.Fprintf(w, "  %s\n", label)
			} else {
				fmt.Fprintln(w, Wrap("  "+label+" "+body, width-2))
			}
		case sectionCode:
			fmt.Fprintln(w, "  "+Dim("```"))
			for _, line := range strings.Split(s.text, "\n") {
				fmt.Fprintln(w, "  "+line)
			}
			fmt.Fprintln(w, "  "+Dim("```"))
		}
	}
}

type commentSection struct {
	kind  int // sectionText, sectionLabel, or sectionCode
	label string
	text  string
}

const (
	sectionText  = 0
	sectionLabel = 1
	sectionCode  = 2
)

// parseCommentSections splits a comment into typed sections: labeled blocks
// (**Observation:**, **Rationale:**, **Fix:**), code fences (```), and plain text.
// Each label starts a new section when encountered at the start of a line.
func parseCommentSections(comment string) []commentSection {
	var sections []commentSection
	lines := strings.Split(comment, "\n")

	inCode := false
	var codeLines []string
	var textLines []string

	flushText := func() {
		if len(textLines) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(textLines, "\n"))
		textLines = textLines[:0]

		// Check if this text block starts with a label like **Observation:**
		label, rest := extractLabel(text)
		if label != "" {
			sections = append(sections, commentSection{kind: sectionLabel, label: label, text: rest})
		} else {
			sections = append(sections, commentSection{kind: sectionText, text: text})
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				sections = append(sections, commentSection{kind: sectionCode, text: strings.Join(codeLines, "\n")})
				codeLines = codeLines[:0]
				inCode = false
			} else {
				flushText()
				inCode = true
			}
			continue
		}
		if inCode {
			codeLines = append(codeLines, line)
			continue
		}
		// If this line starts a new label, flush pending text first.
		if strings.HasPrefix(trimmed, "**") && len(textLines) > 0 {
			flushText()
		}
		textLines = append(textLines, line)
	}
	flushText()
	if inCode && len(codeLines) > 0 {
		sections = append(sections, commentSection{kind: sectionCode, text: strings.Join(codeLines, "\n")})
	}
	return sections
}

// extractLabel checks if text starts with a **Label:** prefix and returns the
// label and the remaining text.
func extractLabel(text string) (label, rest string) {
	if !strings.HasPrefix(text, "**") {
		return "", text
	}
	end := strings.Index(text[2:], "**")
	if end < 0 {
		return "", text
	}
	label = text[2 : 2+end]
	rest = strings.TrimSpace(text[2+end+2:])
	// Label must end with ":" to be treated as a section label
	if !strings.HasSuffix(label, ":") {
		return "", text
	}
	label = strings.TrimSuffix(label, ":")
	return label, rest
}

// indent prefixes each line of s with prefix.
func indent(s, prefix string) string {
	if s == "" {
		return s
	}
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
