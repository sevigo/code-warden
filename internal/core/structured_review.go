package core

// Suggestion represents a single piece of feedback for a specific line of code.
type Suggestion struct {
	FilePath         string `json:"file_path"`
	StartLine        int    `json:"start_line,omitempty"` // For multi-line suggestions
	LineNumber       int    `json:"line_number"`
	Severity         string `json:"severity"` // e.g., "Low", "Medium", "High", "Critical"
	Category         string `json:"category"` // e.g., "Best Practice", "Bug", "Style", "Security"
	Comment          string `json:"comment"`
	Confidence       int    `json:"confidence,omitempty"`
	EstimatedFixTime string `json:"estimated_fix_time,omitempty"`
	Reproducibility  string `json:"reproducibility,omitempty"`
	CodeSuggestion   string `json:"code_suggestion,omitempty"` // Raw code fix from LLM
}

// StructuredReview represents the full review output from the LLM in a parsable format.
type StructuredReview struct {
	Title       string       `json:"title,omitempty"` // For distinct headers (e.g. "Re-Review Summary")
	Summary     string       `json:"summary"`
	Verdict     string       `json:"verdict,omitempty"` // Added for programmatic approval status
	Confidence  int          `json:"confidence,omitempty"`
	Suggestions []Suggestion `json:"suggestions"`
}

// ReReviewResult represents the structured output expected from the LLM for a re-review.
type ReReviewResult struct {
	Verdict string `json:"verdict"` // "APPROVE", "REQUEST_CHANGES", "COMMENT"
	Summary string `json:"summary"`
}
