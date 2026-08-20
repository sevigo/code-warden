package review

import (
	"fmt"

	"github.com/sevigo/code-warden/internal/core"
)

// CoverageStatus is the aggregate completion state of a review run.
type CoverageStatus string

const (
	CoverageStatusComplete CoverageStatus = "complete"
	CoverageStatusPartial  CoverageStatus = "partial"
	CoverageStatusSkipped  CoverageStatus = "skipped"
)

const (
	CoverageItemCompleted   = "completed"
	CoverageItemPartial     = "partial"
	CoverageItemSkipped     = "skipped"
	CoverageItemReviewed    = "reviewed"
	CoverageItemIgnored     = "ignored"
	CoverageItemNotReviewed = "not_reviewed"
)

// CoverageReceipt records what the review engine did and did not examine.
type CoverageReceipt struct {
	Status CoverageStatus  `json:"status"`
	Files  []FileCoverage  `json:"files"`
	Angles []AngleCoverage `json:"angles"`
	Notes  []string        `json:"notes,omitempty"`
}

// FileCoverage describes whether one changed file was reviewed.
type FileCoverage struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// AngleCoverage describes the observable outcome of one configured angle.
type AngleCoverage struct {
	Angle             string `json:"angle"`
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	Iterations        int    `json:"iterations,omitempty"`
	TokensIn          int    `json:"tokens_in,omitempty"`
	TokensOut         int    `json:"tokens_out,omitempty"`
	CandidateFindings int    `json:"candidate_findings"`
}

func buildCoverageReceipt(allFiles, reviewedFiles []core.ChangedFile, allAngles, enabledAngles, scopedAngles []Angle, results []AngleResult, skipReason string) *CoverageReceipt {
	receipt := &CoverageReceipt{Status: CoverageStatusComplete}
	receipt.Files = buildFileCoverage(allFiles, reviewedFiles, skipReason)
	receipt.Angles = buildAngleCoverage(allAngles, enabledAngles, scopedAngles, results, skipReason)

	if skipReason != "" {
		receipt.Status = CoverageStatusSkipped
		receipt.Notes = []string{skipReason}
		return receipt
	}
	for _, file := range receipt.Files {
		if file.Status == CoverageItemNotReviewed {
			receipt.Status = CoverageStatusPartial
			receipt.Notes = append(receipt.Notes, fmt.Sprintf("%s was not reviewed", file.Path))
		}
	}
	for _, angle := range receipt.Angles {
		if angle.Status == CoverageItemPartial || angle.Status == CoverageItemNotReviewed {
			receipt.Status = CoverageStatusPartial
			receipt.Notes = append(receipt.Notes, fmt.Sprintf("%s angle did not complete normally", angle.Angle))
		}
	}
	return receipt
}

func buildFileCoverage(allFiles, reviewedFiles []core.ChangedFile, skipReason string) []FileCoverage {
	reviewed := make(map[string]struct{}, len(reviewedFiles))
	for _, file := range reviewedFiles {
		reviewed[file.Filename] = struct{}{}
	}

	coverage := make([]FileCoverage, 0, len(allFiles))
	for _, file := range allFiles {
		if skipReason == "no code changes" {
			coverage = append(coverage, FileCoverage{Path: file.Filename, Status: CoverageItemNotReviewed, Reason: skipReason})
			continue
		}
		_, eligible := reviewed[file.Filename]
		switch {
		case eligible && skipReason == "":
			coverage = append(coverage, FileCoverage{Path: file.Filename, Status: CoverageItemReviewed})
		case eligible:
			coverage = append(coverage, FileCoverage{Path: file.Filename, Status: CoverageItemNotReviewed, Reason: skipReason})
		default:
			coverage = append(coverage, FileCoverage{Path: file.Filename, Status: CoverageItemIgnored, Reason: "matched an ignore pattern"})
		}
	}
	return coverage
}

func buildAngleCoverage(allAngles, enabledAngles, scopedAngles []Angle, results []AngleResult, skipReason string) []AngleCoverage {
	enabled := angleSet(enabledAngles)
	scoped := angleSet(scopedAngles)
	resultByAngle := make(map[string]AngleResult, len(results))
	for _, result := range results {
		resultByAngle[result.Angle] = result
	}

	coverage := make([]AngleCoverage, 0, len(allAngles))
	for _, angle := range allAngles {
		if skipReason == "no review angles enabled" {
			if _, ok := enabled[angle.Name]; !ok {
				coverage = append(coverage, AngleCoverage{Angle: angle.Name, Status: CoverageItemSkipped, Reason: "disabled by review configuration"})
				continue
			}
		}
		if skipReason != "" {
			coverage = append(coverage, AngleCoverage{Angle: angle.Name, Status: CoverageItemSkipped, Reason: skipReason})
			continue
		}
		if _, ok := enabled[angle.Name]; !ok {
			coverage = append(coverage, AngleCoverage{Angle: angle.Name, Status: CoverageItemSkipped, Reason: "disabled by review configuration"})
			continue
		}
		if _, ok := scoped[angle.Name]; !ok {
			coverage = append(coverage, AngleCoverage{Angle: angle.Name, Status: CoverageItemSkipped, Reason: "not relevant to changed files"})
			continue
		}
		result, ok := resultByAngle[angle.Name]
		if !ok {
			coverage = append(coverage, AngleCoverage{Angle: angle.Name, Status: CoverageItemNotReviewed, Reason: "no result collected"})
			continue
		}
		status := CoverageItemCompleted
		reason := ""
		switch result.Status {
		case AngleStatusCompleted:
		case AngleStatusPartial:
			status = CoverageItemPartial
			reason = "iteration limit reached; partial response parsed"
		default:
			status = CoverageItemNotReviewed
			reason = "executor returned an unknown completion status"
		}
		coverage = append(coverage, AngleCoverage{
			Angle:             angle.Name,
			Status:            status,
			Reason:            reason,
			Iterations:        result.Iterations,
			TokensIn:          result.TokensIn,
			TokensOut:         result.TokensOut,
			CandidateFindings: len(result.Suggestions),
		})
	}
	return coverage
}

func angleSet(angles []Angle) map[string]struct{} {
	set := make(map[string]struct{}, len(angles))
	for _, angle := range angles {
		set[angle.Name] = struct{}{}
	}
	return set
}
