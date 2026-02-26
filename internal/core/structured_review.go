// Package core defines the essential interfaces and data structures that form the
// backbone of the application. These components are designed to be abstract,
// allowing for flexible and decoupled implementations of the application's logic.
package core

// Suggestion represents a single piece of feedback for a specific line of code.
// It contains the location, severity, and description of a potential issue,
// along with optional code suggestions for fixing the problem.
type Suggestion struct {
	// FilePath is the path to the file containing the issue, relative to the repository root.
	FilePath string `json:"file_path" xml:"file"`
	// StartLine is the first line of a multi-line suggestion, or 0 if not applicable.
	StartLine int `json:"start_line,omitempty" xml:"start_line,omitempty"`
	// LineNumber is the line number where the issue occurs.
	LineNumber int `json:"line_number" xml:"line"`
	// Severity indicates the impact level of the issue.
	// Common values are "Low", "Medium", "High", and "Critical".
	Severity string `json:"severity" xml:"severity"`
	// Category classifies the type of issue.
	// Common values are "Best Practice", "Bug", "Style", and "Security".
	Category string `json:"category" xml:"category"`
	// Comment is the human-readable description of the issue and its context.
	Comment string `json:"comment" xml:"comment"`
	// Confidence is the LLM's confidence score for this suggestion (0-100).
	Confidence int `json:"confidence,omitempty" xml:"confidence,omitempty"`
	// Reproducibility describes how easily the issue can be reproduced.
	Reproducibility string `json:"reproducibility,omitempty" xml:"reproducibility,omitempty"`
	// CodeSuggestion is the raw code fix proposed by the LLM.
	CodeSuggestion string `json:"code_suggestion,omitempty" xml:"code_suggestion,omitempty"`
	// Source is the citation for where this finding originated (anti-hallucination grounding).
	// Format: "diff:L{line}", "context:{file}:{line}", "inference:{type}", or "external:{description}"
	Source string `json:"source,omitempty" xml:"source,omitempty"`
}

// StructuredReview represents the complete output from the LLM in a structured,
// parsable format. It contains a summary of the overall review, a verdict on
// whether changes should be accepted, and a list of specific suggestions.
type StructuredReview struct {
	// Title is an optional header for the review (e.g., "Re-Review Summary").
	Title string `json:"title,omitempty" xml:"title,omitempty"`
	// Summary is a high-level overview of the review findings.
	Summary string `json:"summary" xml:"summary"`
	// Verdict is the programmatic approval status: "APPROVE", "REQUEST_CHANGES", or "COMMENT".
	Verdict string `json:"verdict,omitempty" xml:"verdict,omitempty"`
	// Confidence is the overall confidence score for this review (0-100).
	Confidence int `json:"confidence,omitempty" xml:"confidence,omitempty"`
	// Suggestions is a list of specific code review feedback items.
	Suggestions []Suggestion `json:"suggestions" xml:"suggestions>suggestion"`
}

// ReReviewResult represents the expected structured output from the LLM
// when performing a follow-up review of changes since a previous review.
type ReReviewResult struct {
	// Verdict is the programmatic approval status: "APPROVE", "REQUEST_CHANGES", or "COMMENT".
	Verdict string `json:"verdict"`
	// Summary is a high-level overview of the re-review findings.
	Summary string `json:"summary"`
}
