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
	badge := renderVerdict(verdict)
	fmt.Fprintln(w, badge)
	fmt.Fprintln(w)

	// Summary
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

// renderFinding writes a single finding as a bordered block.
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
	header := fmt.Sprintf("▸ %s %s", style("["+sev+"]"), Bold(Cyan(loc)))
	fmt.Fprintln(w, header)

	// Category tag, if present
	if f.Category != "" {
		fmt.Fprintln(w, "  "+Dim(f.Category))
	}

	// Comment
	if f.Comment != "" {
		comment := f.Comment
		// Highlight bold markers as a lightweight formatting pass.
		comment = formatComment(comment)
		fmt.Fprintln(w, Wrap("  "+comment, width-2))
	}

	// Code suggestion block
	if f.CodeSuggestion != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  "+GreenBold("Suggestion:"))
		for _, line := range strings.Split(f.CodeSuggestion, "\n") {
			fmt.Fprintln(w, "    "+Green(line))
		}
	}
}

// formatComment performs a light pass to render markdown bold markers as ANSI
// bold and collapse common prefix labels.
func formatComment(s string) string {
	// Replace **bold** segments with ANSI bold (only when colors enabled).
	if Enabled {
		var b strings.Builder
		rest := s
		for {
			open := strings.Index(rest, "**")
			if open < 0 {
				b.WriteString(rest)
				break
			}
			end := strings.Index(rest[open+2:], "**")
			if end < 0 {
				b.WriteString(rest)
				break
			}
			end += open + 2
			b.WriteString(rest[:open])
			b.WriteString(Bold(rest[open+2 : end]))
			rest = rest[end+2:]
		}
		return b.String()
	}
	return s
}
