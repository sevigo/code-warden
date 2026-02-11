package core

// Suggestion represents a single piece of feedback for a specific line of code.
type Suggestion struct {
	FilePath   string `json:"file_path"`
	StartLine  int    `json:"start_line,omitempty"` // For multi-line suggestions
	LineNumber int    `json:"line_number"`
	Severity   string `json:"severity"` // e.g., "Low", "Medium", "High", "Critical"
	Category   string `json:"category"` // e.g., "Best Practice", "Bug", "Style", "Security"
	Comment    string `json:"comment"`
}

// StructuredReview represents the full review output from the LLM in a parsable format.
type StructuredReview struct {
	Summary     string       `json:"summary"`
	Verdict     string       `json:"verdict,omitempty"` // Added for programmatic approval status
	Suggestions []Suggestion `json:"suggestions"`
}
