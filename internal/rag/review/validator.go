package review

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

var severityRank = map[string]int{
	"Critical": 4,
	"High":     3,
	"Medium":   2,
	"Low":      1,
}

type SuggestionValidator struct {
	diffContent  string
	changedFiles []internalgithub.ChangedFile
	fileLines    map[string]map[int]struct{}
}

func NewSuggestionValidator(diffContent string, changedFiles []internalgithub.ChangedFile) *SuggestionValidator {
	return &SuggestionValidator{
		diffContent:  diffContent,
		changedFiles: changedFiles,
		fileLines:    BuildValidLineMap(changedFiles),
	}
}

// BuildValidLineMap constructs the valid-line map from changed files' patches.
// It is used by both the review pipeline and the jobs layer for suggestion validation.
func BuildValidLineMap(changedFiles []internalgithub.ChangedFile) map[string]map[int]struct{} {
	result := make(map[string]map[int]struct{})
	for _, cf := range changedFiles {
		lines, err := internalgithub.ParseValidLinesFromPatch(cf.Patch, nil)
		if err != nil {
			continue
		}
		result[cf.Filename] = lines
	}
	return result
}

func (v *SuggestionValidator) ValidateLineNumber(sug *core.Suggestion) bool {
	if sug.FilePath == "" || sug.LineNumber == 0 {
		return true
	}

	cleanPath := strings.TrimPrefix(sug.FilePath, "./")
	lines, exists := v.fileLines[cleanPath]
	if !exists {
		return false
	}

	_, ok := lines[sug.LineNumber]
	return ok
}

func (v *SuggestionValidator) ValidateSource(sug *core.Suggestion) bool {
	if sug.Source == "" {
		return false
	}

	if strings.HasPrefix(sug.Source, "diff:L") {
		lineStr := strings.TrimPrefix(sug.Source, "diff:L")
		line, err := strconv.Atoi(lineStr)
		if err != nil {
			return false
		}
		return v.lineExistsInDiff(sug.FilePath, line)
	}

	if strings.HasPrefix(sug.Source, "context:") {
		return true
	}

	if strings.HasPrefix(sug.Source, "inference:") {
		return true
	}

	if strings.HasPrefix(sug.Source, "external:") {
		return true
	}

	return false
}

func (v *SuggestionValidator) lineExistsInDiff(filename string, line int) bool {
	cleanPath := strings.TrimPrefix(filename, "./")
	lines, exists := v.fileLines[cleanPath]
	if !exists {
		return false
	}
	_, ok := lines[line]
	return ok
}

type SuggestionFilter struct {
	MinConfidence    int
	ValidateSources  bool
	ValidateLineNums bool
	Deduplicate      bool
}

func DefaultFilter() *SuggestionFilter {
	return &SuggestionFilter{
		MinConfidence:    30,
		ValidateSources:  true,
		ValidateLineNums: true,
		Deduplicate:      true,
	}
}

// NewFilterForProfile returns a filter with confidence threshold based on review profile.
func NewFilterForProfile(profile core.ReviewProfile) *SuggestionFilter {
	threshold := profile.MinConfidence()
	return &SuggestionFilter{
		MinConfidence:    threshold,
		ValidateSources:  true,
		ValidateLineNums: true,
		Deduplicate:      true,
	}
}

func (f *SuggestionFilter) FilterAndRank(
	review *core.StructuredReview,
	validator *SuggestionValidator,
	logFunc func(msg string, args ...any),
) *core.StructuredReview {
	if len(review.Suggestions) == 0 {
		return review
	}

	filtered := make([]core.Suggestion, 0, len(review.Suggestions))
	seen := make(map[string]bool)

	for i := range review.Suggestions {
		sug := &review.Suggestions[i]

		if shouldSkip(sug, f.MinConfidence, logFunc) {
			continue
		}

		validateLineNumber(sug, validator, f.ValidateLineNums, logFunc)
		validateSourceCitation(sug, validator, f.ValidateSources, logFunc)
		validateStartLine(sug, logFunc)

		if f.Deduplicate {
			key := makeDedupKey(sug)
			if seen[key] {
				if logFunc != nil {
					logFunc("deduplicating overlapping suggestion", "file", sug.FilePath, "line", sug.LineNumber)
				}
				continue
			}
			seen[key] = true
		}

		filtered = append(filtered, *sug)
	}

	sortBySeverityAndConfidence(filtered)
	review.Suggestions = filtered
	return review
}

func shouldSkip(sug *core.Suggestion, minConfidence int, logFunc func(msg string, args ...any)) bool {
	if sug.Confidence < minConfidence && sug.Severity != "Critical" {
		if logFunc != nil {
			logFunc("filtering low confidence suggestion", "file", sug.FilePath, "line", sug.LineNumber, "confidence", sug.Confidence)
		}
		return true
	}
	return false
}

func validateStartLine(sug *core.Suggestion, logFunc func(msg string, args ...any)) {
	if sug.StartLine <= 0 {
		return // Single-line suggestion, nothing to validate
	}
	if sug.StartLine > sug.LineNumber {
		if logFunc != nil {
			logFunc("normalizing invalid start_line > line_number",
				"file", sug.FilePath,
				"start_line", sug.StartLine,
				"line_number", sug.LineNumber,
			)
		}
		sug.StartLine = 0 // Fall back to single-line
	}
}

func validateLineNumber(sug *core.Suggestion, validator *SuggestionValidator, shouldValidate bool, logFunc func(msg string, args ...any)) {
	if !shouldValidate || validator == nil {
		return
	}
	if !validator.ValidateLineNumber(sug) {
		sug.Confidence = minInt(sug.Confidence, 40)
		if sug.Source == "" {
			sug.Source = "external:line-not-in-diff"
		}
		if logFunc != nil {
			logFunc("suggestion line not in diff", "file", sug.FilePath, "line", sug.LineNumber)
		}
	}
}

func validateSourceCitation(sug *core.Suggestion, validator *SuggestionValidator, shouldValidate bool, logFunc func(msg string, args ...any)) {
	if !shouldValidate || sug.Source == "" {
		return
	}
	if !validator.ValidateSource(sug) {
		sug.Confidence = minInt(sug.Confidence, 50)
		if logFunc != nil {
			logFunc("invalid source citation", "source", sug.Source)
		}
	}
}

func makeDedupKey(sug *core.Suggestion) string {
	return strings.ToLower(sug.FilePath + ":" + strconv.Itoa(sug.LineNumber) + ":" + sug.Category)
}

func sortBySeverityAndConfidence(suggestions []core.Suggestion) {
	slices.SortFunc(suggestions, func(a, b core.Suggestion) int {
		severityDiff := severityRank[b.Severity] - severityRank[a.Severity]
		if severityDiff != 0 {
			return severityDiff
		}
		return b.Confidence - a.Confidence
	})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
