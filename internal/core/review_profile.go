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
	HighRisk      bool          `json:"high_risk"`
	ProfileReason string        `json:"profile_reason"`
}

// High-risk path segments that always trigger thorough review.
// These are matched as whole path segments, not substrings.
var highRiskPaths = []string{
	"auth", "authentication", "authorization",
	"crypto", "cryptography",
	"payment", "checkout", "billing",
	"credential", "credentials",
	"password", "passwd",
	"secret", "secrets",
	"jwt", "session",
	"security", "acl", "rbac", "admin",
	"privkey", "apikey", "privatekey",
	"migration", "migrations",
}

// CalculateProfile computes the review profile based on PR complexity and risk.
// The profile determines review thoroughness and confidence thresholds.
func CalculateProfile(
	linesAdded, linesDeleted int,
	filesChanged int,
	impactRadius int,
	testCoverage bool,
	docsOnly bool,
	changedFilePaths []string,
) ComplexityScore {
	// Check for high-risk paths first - these always get thorough review
	highRisk := hasHighRiskPath(changedFilePaths)

	// Calculate magnitude score (round up so 49 lines = 1, not 0)
	linesScore := (linesAdded + linesDeleted + 49) / 50
	fileScore := filesChanged * 2
	impactScore := min(impactRadius*3, 45) // Increased weight, higher cap

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

	highImpact := impactRadius > 20

	var profile ReviewProfile
	var reason string

	// High-risk or high-impact always forces thorough review
	switch {
	case highRisk:
		profile = ProfileThorough
		reason = "high-risk path detected (auth/crypto/payment/etc)"
	case highImpact:
		profile = ProfileThorough
		reason = "high-impact change affecting many dependents"
	case score <= 15:
		profile = ProfileQuick
		reason = "small or low-impact change"
	case score <= 40:
		profile = ProfileStandard
		reason = "moderate change"
	default:
		profile = ProfileThorough
		reason = "substantial change"
	}

	return ComplexityScore{
		LinesChanged:  linesAdded + linesDeleted,
		FilesChanged:  filesChanged,
		ImpactRadius:  impactRadius,
		TestCoverage:  testCoverage,
		DocsOnly:      docsOnly,
		Score:         score,
		Profile:       profile,
		HighImpact:    highImpact,
		HighRisk:      highRisk,
		ProfileReason: reason,
	}
}

func (p ReviewProfile) String() string {
	return string(p)
}

// MinConfidence returns the minimum confidence threshold for this profile.
// All profiles use the same threshold - the prompt instructions control scope.
func (p ReviewProfile) MinConfidence() int {
	return 40 // Uniform threshold - let prompt control intensity
}

// hasHighRiskPath checks if any path segment matches high-risk keywords.
// Uses path segment matching, not substring matching, to avoid false positives.
func hasHighRiskPath(paths []string) bool {
	for _, path := range paths {
		// Split path into segments by common separators
		parts := strings.FieldsFunc(strings.ToLower(path), func(r rune) bool {
			return r == '/' || r == '\\' || r == '_' || r == '.' || r == '-'
		})
		for _, part := range parts {
			for _, risk := range highRiskPaths {
				if part == risk {
					return true
				}
			}
		}
	}
	return false
}

func IsTestFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "_test.") ||
		strings.Contains(lower, "_test.go") ||
		strings.Contains(lower, ".test.go") ||
		strings.Contains(lower, "_spec.go") ||
		strings.Contains(lower, "/tests/") ||
		strings.Contains(lower, "\\tests\\") ||
		strings.HasPrefix(lower, "tests/") ||
		strings.Contains(lower, "/__tests__/") ||
		strings.Contains(lower, "\\__tests__\\") ||
		strings.HasPrefix(lower, "__tests__/")
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
