package core

import (
	"strings"
)

type ReviewProfile string

const (
	ProfileQuick    ReviewProfile = "quick"
	ProfileStandard ReviewProfile = "standard"
	ProfileThorough ReviewProfile = "thorough"
)

type ComplexityScore struct {
	LinesChanged  int           `json:"lines_changed"`
	FilesChanged  int           `json:"files_changed"`
	ImpactRadius  int           `json:"impact_radius"`
	TestCoverage  bool          `json:"test_coverage"`
	DocsOnly      bool          `json:"docs_only"`
	Score         int           `json:"score"`
	Profile       ReviewProfile `json:"profile"`
	HighImpact    bool          `json:"high_impact"`
	ProfileReason string        `json:"profile_reason"`
}

var ProfileThresholds = map[ReviewProfile]int{
	ProfileQuick:    50,
	ProfileStandard: 40,
	ProfileThorough: 35,
}

func CalculateProfile(
	linesAdded, linesDeleted int,
	filesChanged int,
	impactRadius int,
	testCoverage bool,
	docsOnly bool,
) ComplexityScore {
	linesScore := (linesAdded + linesDeleted) / 50
	fileScore := filesChanged * 2
	impactScore := minInt(impactRadius*2, 30)

	score := linesScore + fileScore + impactScore

	if testCoverage {
		score -= 10
	}
	if docsOnly {
		score -= 20
		if score < 0 {
			score = 0
		}
	}

	var profile ReviewProfile
	var reason string

	switch {
	case score <= 15:
		profile = ProfileQuick
		reason = "small or low-impact change"
	case score <= 40:
		profile = ProfileStandard
		reason = "moderate change"
	default:
		profile = ProfileThorough
		reason = "substantial or high-impact change"
	}

	highImpact := impactRadius > 20

	return ComplexityScore{
		LinesChanged:  linesAdded + linesDeleted,
		FilesChanged:  filesChanged,
		ImpactRadius:  impactRadius,
		TestCoverage:  testCoverage,
		DocsOnly:      docsOnly,
		Score:         score,
		Profile:       profile,
		HighImpact:    highImpact,
		ProfileReason: reason,
	}
}

func (p ReviewProfile) String() string {
	return string(p)
}

func (p ReviewProfile) MinConfidence() int {
	if threshold, ok := ProfileThresholds[p]; ok {
		return threshold
	}
	return 35
}

func IsTestFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "_test.") ||
		strings.Contains(lower, "_test.go") ||
		strings.Contains(lower, "/tests/") ||
		strings.Contains(lower, "\\tests\\") ||
		strings.Contains(lower, "/__tests__/") ||
		strings.Contains(lower, "\\__tests__\\") ||
		strings.HasSuffix(lower, ".test.go") ||
		strings.HasSuffix(lower, "_spec.go")
}

func IsDocsFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".md") ||
		strings.HasSuffix(lower, ".rst") ||
		strings.HasSuffix(lower, ".txt") ||
		strings.Contains(lower, "/docs/") ||
		strings.Contains(lower, "\\docs\\")
}

func CountNonTestFiles(sources []string) int {
	count := 0
	for _, path := range sources {
		if !IsTestFile(path) {
			count++
		}
	}
	return count
}

func HasTestCoverage(changedFiles []string) bool {
	for _, f := range changedFiles {
		if IsTestFile(f) {
			return true
		}
	}
	return false
}

func IsDocsOnly(changedFiles []string) bool {
	if len(changedFiles) == 0 {
		return false
	}
	for _, f := range changedFiles {
		if !IsDocsFile(f) {
			return false
		}
	}
	return true
}

// minInt returns the smaller of two integers.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
