package review

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
)

func TestBuildCoverageReceiptDistinguishesIgnoredAndUnreviewedFiles(t *testing.T) {
	t.Parallel()

	files := []core.ChangedFile{{Filename: "main.go"}, {Filename: "generated.go"}}
	angles := []Angle{{Name: "bug"}}
	receipt := buildCoverageReceipt(files, files[:1], angles, angles, nil, nil, "too many changed files")

	assert.Equal(t, CoverageStatusSkipped, receipt.Status)
	require.Len(t, receipt.Files, 2)
	assert.Equal(t, FileCoverage{Path: "main.go", Status: CoverageItemNotReviewed, Reason: "too many changed files"}, receipt.Files[0])
	assert.Equal(t, FileCoverage{Path: "generated.go", Status: CoverageItemIgnored, Reason: "matched an ignore pattern"}, receipt.Files[1])
	assert.Equal(t, CoverageItemSkipped, receipt.Angles[0].Status)
}

func TestBuildCoverageReceiptMarksChangedFilesUnreviewedWithoutDiff(t *testing.T) {
	t.Parallel()

	files := []core.ChangedFile{{Filename: "main.go"}}
	receipt := buildCoverageReceipt(files, nil, nil, nil, nil, nil, "no code changes")

	assert.Equal(t, CoverageStatusSkipped, receipt.Status)
	require.Len(t, receipt.Files, 1)
	assert.Equal(t, CoverageItemNotReviewed, receipt.Files[0].Status)
	assert.Equal(t, "no code changes", receipt.Files[0].Reason)
}

func TestBuildCoverageReceiptCanBeCompleteWithIntentionallySkippedAngles(t *testing.T) {
	t.Parallel()

	files := []core.ChangedFile{{Filename: "main.go"}}
	allAngles := []Angle{{Name: "bug"}, {Name: "security"}}
	enabledAngles := allAngles[:1]
	results := []AngleResult{{Angle: "bug", Status: AngleStatusCompleted}}
	receipt := buildCoverageReceipt(files, files, allAngles, enabledAngles, enabledAngles, results, "")

	assert.Equal(t, CoverageStatusComplete, receipt.Status)
	assert.Equal(t, CoverageItemCompleted, receipt.Angles[0].Status)
	assert.Equal(t, CoverageItemSkipped, receipt.Angles[1].Status)
	assert.Equal(t, "disabled by review configuration", receipt.Angles[1].Reason)
}
