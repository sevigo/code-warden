package render

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
)

func TestRenderCoverageShowsCompletedAndPartialWork(t *testing.T) {
	t.Parallel()

	receipt := &agentreview.CoverageReceipt{
		Status: agentreview.CoverageStatusPartial,
		Files: []agentreview.FileCoverage{
			{Path: "main.go", Status: agentreview.CoverageItemReviewed},
			{Path: "generated.go", Status: agentreview.CoverageItemIgnored},
		},
		Angles: []agentreview.AngleCoverage{
			{Angle: "bug", Status: agentreview.CoverageItemCompleted, Iterations: 3, TokensIn: 800, TokensOut: 120, CandidateFindings: 1},
			{Angle: "security", Status: agentreview.CoverageItemPartial, Reason: "iteration limit reached", Iterations: 8},
			{Angle: "performance", Status: agentreview.CoverageItemSkipped, Reason: "not relevant to changed files"},
		},
		Notes: []string{"security angle did not complete normally"},
	}

	var output bytes.Buffer
	Coverage(&output, receipt)

	assert.Contains(t, output.String(), "COVERAGE: PARTIAL")
	assert.Contains(t, output.String(), "FILES: 1 reviewed, 1 ignored, 0 not reviewed")
	assert.Contains(t, output.String(), "generated.go: ignored")
	assert.Contains(t, output.String(), "bug: completed (3 iterations, 800 in/120 out tokens, 1 candidate)")
	assert.Contains(t, output.String(), "security: partial")
	assert.Contains(t, output.String(), "performance: skipped")
	assert.Contains(t, output.String(), "NOTE: security angle did not complete normally")
}
