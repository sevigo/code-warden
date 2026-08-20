package render

import (
	"fmt"
	"io"
	"strings"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
)

// Coverage writes a concise receipt showing which files and review angles
// were actually examined.
func Coverage(w io.Writer, receipt *agentreview.CoverageReceipt) {
	if receipt == nil {
		return
	}

	reviewed, ignored, notReviewed := fileCoverageCounts(receipt.Files)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "COVERAGE: %s\n", strings.ToUpper(string(receipt.Status)))
	fmt.Fprintf(w, "FILES: %d reviewed, %d ignored, %d not reviewed\n", reviewed, ignored, notReviewed)
	for _, file := range receipt.Files {
		if file.Status == agentreview.CoverageItemReviewed {
			continue
		}
		fmt.Fprintf(w, "- %s: %s", file.Path, file.Status)
		if file.Reason != "" {
			fmt.Fprintf(w, " — %s", file.Reason)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "ANGLES:")
	for _, angle := range receipt.Angles {
		fmt.Fprintf(w, "- %s: %s", angle.Angle, angle.Status)
		if angle.Status == agentreview.CoverageItemCompleted || angle.Status == agentreview.CoverageItemPartial {
			fmt.Fprintf(w, " (%d iterations, %d in/%d out tokens, %d %s)",
				angle.Iterations, angle.TokensIn, angle.TokensOut, angle.CandidateFindings,
				pluralize("candidate", angle.CandidateFindings))
		}
		if angle.Reason != "" {
			fmt.Fprintf(w, " — %s", angle.Reason)
		}
		fmt.Fprintln(w)
	}
	for _, note := range receipt.Notes {
		fmt.Fprintf(w, "NOTE: %s\n", note)
	}
}

func pluralize(word string, count int) string {
	if count == 1 {
		return word
	}
	return word + "s"
}

func fileCoverageCounts(files []agentreview.FileCoverage) (reviewed, ignored, notReviewed int) {
	for _, file := range files {
		switch file.Status {
		case agentreview.CoverageItemReviewed:
			reviewed++
		case agentreview.CoverageItemIgnored:
			ignored++
		case agentreview.CoverageItemNotReviewed:
			notReviewed++
		}
	}
	return reviewed, ignored, notReviewed
}
