package core

import (
	"testing"
)

func TestCalculateProfile(t *testing.T) {
	tests := []struct {
		name           string
		linesAdded     int
		linesDeleted   int
		filesChanged   int
		impactRadius   int
		testCoverage   bool
		docsOnly       bool
		changedPaths   []string
		wantProfile    ReviewProfile
		wantHighImpact bool
		wantHighRisk   bool
	}{
		{
			name:           "small change - quick profile",
			linesAdded:     10,
			linesDeleted:   5,
			filesChanged:   1,
			impactRadius:   0,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"utils/helper.go"},
			wantProfile:    ProfileQuick,
			wantHighImpact: false,
			wantHighRisk:   false,
		},
		{
			name:           "moderate change - standard profile",
			linesAdded:     100,
			linesDeleted:   50,
			filesChanged:   3,
			impactRadius:   5,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"api/handler.go", "api/routes.go", "api/service.go"},
			wantProfile:    ProfileStandard,
			wantHighImpact: false,
			wantHighRisk:   false,
		},
		{
			name:           "large change - thorough profile",
			linesAdded:     300,
			linesDeleted:   100,
			filesChanged:   10,
			impactRadius:   15,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"cmd/server/main.go"},
			wantProfile:    ProfileThorough,
			wantHighImpact: false,
			wantHighRisk:   false,
		},
		{
			name:           "high impact - forces thorough",
			linesAdded:     5,
			linesDeleted:   0,
			filesChanged:   1,
			impactRadius:   25,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"internal/core/database.go"},
			wantProfile:    ProfileThorough,
			wantHighImpact: true,
			wantHighRisk:   false,
		},
		{
			name:           "auth path - high risk forces thorough",
			linesAdded:     5,
			linesDeleted:   0,
			filesChanged:   1,
			impactRadius:   0,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"internal/auth/jwt.go"},
			wantProfile:    ProfileThorough,
			wantHighImpact: false,
			wantHighRisk:   true,
		},
		{
			name:           "payment path - high risk",
			linesAdded:     10,
			linesDeleted:   5,
			filesChanged:   1,
			impactRadius:   0,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"services/payment/processor.go"},
			wantProfile:    ProfileThorough,
			wantHighImpact: false,
			wantHighRisk:   true,
		},
		{
			name:           "docs only - quick profile",
			linesAdded:     50,
			linesDeleted:   20,
			filesChanged:   2,
			impactRadius:   0,
			testCoverage:   false,
			docsOnly:       true,
			changedPaths:   []string{"README.md", "docs/api.md"},
			wantProfile:    ProfileQuick,
			wantHighImpact: false,
			wantHighRisk:   false,
		},
		{
			name:           "with test coverage - score reduced",
			linesAdded:     100,
			linesDeleted:   50,
			filesChanged:   2,
			impactRadius:   5,
			testCoverage:   true,
			docsOnly:       false,
			changedPaths:   []string{"api/handler.go", "api/handler_test.go"},
			wantProfile:    ProfileQuick,
			wantHighImpact: false,
			wantHighRisk:   false,
		},
		{
			name:           "49 lines - rounds up to 1",
			linesAdded:     49,
			linesDeleted:   0,
			filesChanged:   1,
			impactRadius:   0,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"utils/helper.go"},
			wantProfile:    ProfileQuick,
			wantHighImpact: false,
			wantHighRisk:   false,
		},
		{
			name:           "51 lines - scores 1",
			linesAdded:     51,
			linesDeleted:   0,
			filesChanged:   1,
			impactRadius:   0,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"utils/helper.go"},
			wantProfile:    ProfileQuick,
			wantHighImpact: false,
			wantHighRisk:   false,
		},
		{
			name:           "crypto middleware - high risk",
			linesAdded:     20,
			linesDeleted:   10,
			filesChanged:   1,
			impactRadius:   0,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"pkg/crypto/middleware.go"},
			wantProfile:    ProfileThorough,
			wantHighImpact: false,
			wantHighRisk:   true,
		},
		{
			name:           "sql migration - high risk",
			linesAdded:     30,
			linesDeleted:   0,
			filesChanged:   1,
			impactRadius:   0,
			testCoverage:   false,
			docsOnly:       false,
			changedPaths:   []string{"migrations/001_create_users.sql"},
			wantProfile:    ProfileThorough,
			wantHighImpact: false,
			wantHighRisk:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateProfile(
				tt.linesAdded,
				tt.linesDeleted,
				tt.filesChanged,
				tt.impactRadius,
				tt.testCoverage,
				tt.docsOnly,
				tt.changedPaths,
			)

			if got.Profile != tt.wantProfile {
				t.Errorf("CalculateProfile() profile = %v, want %v (score=%d, reason=%s)",
					got.Profile, tt.wantProfile, got.Score, got.ProfileReason)
			}

			if got.HighImpact != tt.wantHighImpact {
				t.Errorf("CalculateProfile() highImpact = %v, want %v", got.HighImpact, tt.wantHighImpact)
			}

			if got.HighRisk != tt.wantHighRisk {
				t.Errorf("CalculateProfile() highRisk = %v, want %v", got.HighRisk, tt.wantHighRisk)
			}
		})
	}
}

func TestIsTestFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"foo_test.go", true},
		{"foo.test.go", true},
		{"foo_spec.go", true},
		{"tests/foo.go", true},
		{"__tests__/foo.go", true},
		{"foo.go", false},
		{"internal/handler.go", false},
		{"internal/handler_test.go", true},
		{"pkg/service_test.go", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsTestFile(tt.path); got != tt.want {
				t.Errorf("IsTestFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsDocsFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"README.md", true},
		{"docs/api.md", true},
		{"docs/README.txt", true},
		{"CHANGELOG.rst", true},
		{"main.go", false},
		{"internal/handler.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsDocsFile(tt.path); got != tt.want {
				t.Errorf("IsDocsFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestMinConfidence(t *testing.T) {
	// All profiles now use uniform threshold of 40
	if got := ProfileQuick.MinConfidence(); got != 40 {
		t.Errorf("ProfileQuick.MinConfidence() = %v, want 40", got)
	}
	if got := ProfileStandard.MinConfidence(); got != 40 {
		t.Errorf("ProfileStandard.MinConfidence() = %v, want 40", got)
	}
	if got := ProfileThorough.MinConfidence(); got != 40 {
		t.Errorf("ProfileThorough.MinConfidence() = %v, want 40", got)
	}
}

func TestHasHighRiskPath(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  bool
	}{
		{"no risky paths", []string{"foo.go", "bar/baz.go"}, false},
		{"auth path", []string{"internal/auth/jwt.go"}, true},
		{"crypto path", []string{"pkg/crypto/encrypt.go"}, true},
		{"payment path", []string{"services/payment/handler.go"}, true},
		{"sql migration", []string{"migrations/001.sql"}, true},
		{"security middleware", []string{"middleware/security.go"}, true},
		{"mixed paths", []string{"foo.go", "internal/auth/handler.go"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasHighRiskPath(tt.paths); got != tt.want {
				t.Errorf("hasHighRiskPath(%v) = %v, want %v", tt.paths, got, tt.want)
			}
		})
	}
}
