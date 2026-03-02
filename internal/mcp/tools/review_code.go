package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
)

// Input validation limits.
const (
	maxDiffLength  = 1000000 // 1MB max diff
	maxTitleLength = 500
)

// ReviewCode performs an internal code review.
type ReviewCode struct {
	RagService rag.Service
	Repo       *storage.Repository
	RepoConfig *core.RepoConfig
	Logger     *slog.Logger
}

// ReviewCodeResponse is the response for review_code tool.
type ReviewCodeResponse struct {
	Verdict     string            `json:"verdict"`
	Confidence  int               `json:"confidence"`
	Summary     string            `json:"summary"`
	Suggestions []core.Suggestion `json:"suggestions,omitempty"`
}

func (t *ReviewCode) Name() string {
	return "review_code"
}

func (t *ReviewCode) Description() string {
	return `Perform an internal code review on a diff.
Returns structured feedback with suggestions and verdict.
Use this to validate your changes before creating a PR.`
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
	t.Logger.Info("review_code: executing tool", "args", args)
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

	// Generate the review
	review, _, err := t.RagService.GenerateReview(ctx, t.RepoConfig, t.Repo, event, diff, nil)
	if err != nil {
		t.Logger.Error("internal review failed", "error", err)
		return nil, fmt.Errorf("review failed: %w", err)
	}

	return ReviewCodeResponse{
		Verdict:     review.Verdict,
		Confidence:  review.Confidence,
		Summary:     review.Summary,
		Suggestions: review.Suggestions,
	}, nil
}
