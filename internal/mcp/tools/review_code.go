package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/rag/review"
	"github.com/sevigo/code-warden/internal/storage"
)

// Input validation limits.
const (
	maxDiffLength  = 1000000 // 1MB max diff
	maxTitleLength = 500
)

// ReviewCode performs an internal code review.
type ReviewCode struct {
	RagService       rag.Service
	Repo             *storage.Repository
	RepoConfig       *core.RepoConfig
	ComparisonModels []string // Models for consensus review
	ReviewsDir       string   // Directory to save review artifacts
	// ReviewTracker records review results for PR enforcement.
	// Always provided by the MCP server. The nil check is defensive programming.
	ReviewTracker    ReviewTracker
	Logger           *slog.Logger
}

// ReviewCodeResponse is the response for review_code tool.
type ReviewCodeResponse struct {
	Verdict     string            `json:"verdict"`
	Confidence  int               `json:"confidence"`
	Summary     string            `json:"summary"`
	Suggestions []core.Suggestion `json:"suggestions,omitempty"`
	DiffHash    string            `json:"diff_hash,omitempty"` // Hash for tracking changes
	ModelsUsed  []string          `json:"models_used,omitempty"`
}

func (t *ReviewCode) Name() string {
	return "review_code"
}

func (t *ReviewCode) Description() string {
	return `Perform an internal code review on a diff.
Returns structured feedback with suggestions and verdict.
Verdict values:
- "APPROVE" - Code is approved, proceed to create PR
- "REQUEST_CHANGES" - Issues found, fix them and review again
- "COMMENT" - General feedback, treat as REQUEST_CHANGES
IMPORTANT: You MUST wait for APPROVE verdict before creating a PR.
Pass the returned diff_hash to create_pull_request to ensure code hasn't changed.`
}

func (t *ReviewCode) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"diff": map[string]any{
				"type":        "string",
				"description": "The git diff to review",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Optional title for the review context",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional description for additional context",
			},
		},
		"required": []string{"diff"},
	}
}

func (t *ReviewCode) Execute(ctx context.Context, args map[string]any) (any, error) {
	t.Logger.Info("review_code: executing tool")
	diff, ok := args["diff"].(string)
	if !ok || diff == "" {
		return nil, fmt.Errorf("diff is required")
	}
	if len(diff) > maxDiffLength {
		t.Logger.Warn("review_code: diff too large", "length", len(diff))
		return nil, fmt.Errorf("diff exceeds maximum size of %d bytes", maxDiffLength)
	}

	// Create a mock event for the review
	title, _ := args["title"].(string)
	if title == "" {
		title = "Internal Code Review"
	}
	if len(title) > maxTitleLength {
		title = title[:maxTitleLength]
	}
	description, _ := args["description"].(string)

	event := &core.GitHubEvent{
		PRTitle:        title,
		PRBody:         description,
		RepoFullName:   t.Repo.FullName,
		HeadSHA:        "internal-review",
		PRNumber:       0,
		InstallationID: 0,
	}

	// Generate diff hash for tracking
	diffHash := hashDiff(diff)

	var structuredReview *core.StructuredReview
	var rawReview string
	var err error

	// Use consensus review if comparison models are configured
	if len(t.ComparisonModels) > 0 {
		t.Logger.Info("using consensus review", "models", t.ComparisonModels)
		structuredReview, rawReview, err = t.RagService.GenerateConsensusReview(
			ctx,
			t.RepoConfig,
			t.Repo,
			event,
			t.ComparisonModels,
			diff,
			nil, // changedFiles extracted internally
		)
	} else {
		t.Logger.Info("using single-model review")
		structuredReview, rawReview, err = t.RagService.GenerateReview(
			ctx,
			t.RepoConfig,
			t.Repo,
			event,
			diff,
			nil,
		)
	}

	if err != nil {
		t.Logger.Error("internal review failed", "error", err)
		return nil, fmt.Errorf("review failed: %w", err)
	}

	// Save review artifact if reviews directory is configured
	if t.ReviewsDir != "" && rawReview != "" {
		ts := time.Now().Format("20060102-150405")
		result := review.ComparisonResult{
			Model:    "internal-review",
			Review:   rawReview,
			Duration: 0,
			Error:    nil,
		}
		review.SaveReviewArtifact(t.Logger, t.ReviewsDir, result, event, ts)
	}

	// Record the review result for PR enforcement
	if t.ReviewTracker != nil {
		t.ReviewTracker.RecordReview(structuredReview.Verdict, diffHash)
	}

	t.Logger.Info("review completed",
		"verdict", structuredReview.Verdict,
		"confidence", structuredReview.Confidence,
		"suggestions", len(structuredReview.Suggestions),
		"diff_hash", diffHash,
	)

	response := ReviewCodeResponse{
		Verdict:     structuredReview.Verdict,
		Confidence:  structuredReview.Confidence,
		Summary:     structuredReview.Summary,
		Suggestions: structuredReview.Suggestions,
		DiffHash:    diffHash,
	}

	if len(t.ComparisonModels) > 0 {
		response.ModelsUsed = t.ComparisonModels
	}

	return response, nil
}

// hashDiff creates a short hash of the diff for tracking changes.
func hashDiff(diff string) string {
	h := sha256.Sum256([]byte(diff))
	return hex.EncodeToString(h[:])
}