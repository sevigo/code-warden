// Package review provides a unified review execution flow used by all
// review entry points (webhook, MCP tool, CLI).
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/rag"
	ragReview "github.com/sevigo/code-warden/internal/rag/review"
	"github.com/sevigo/code-warden/internal/storage"
)

// Config holds configuration for the review executor.
type Config struct {
	// ComparisonModels are models for consensus review.
	// If empty, a single-model review is performed.
	ComparisonModels []string

	// ReviewsDir is the directory to save review artifacts.
	// If empty, no artifacts are saved.
	ReviewsDir string

	// Logger for structured logging.
	Logger *slog.Logger
}

// Params holds the inputs for a review execution.
type Params struct {
	RepoConfig   *core.RepoConfig
	Repo         *storage.Repository
	Event        *core.GitHubEvent
	Diff         string
	ChangedFiles []internalgithub.ChangedFile
}

// Result holds the outputs from a review execution.
type Result struct {
	// Review is the parsed structured review.
	Review *core.StructuredReview

	// RawReview is the raw LLM output.
	RawReview string

	// DiffHash is the SHA-256 hex hash of the reviewed diff.
	DiffHash string

	// ModelsUsed lists the comparison models used (empty for single-model).
	ModelsUsed []string
}

// Executor runs code reviews using either single-model or consensus mode.
type Executor struct {
	ragService rag.Service
	config     Config
}

// NewExecutor creates a new review executor.
func NewExecutor(ragService rag.Service, config Config) *Executor {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Executor{
		ragService: ragService,
		config:     config,
	}
}

// Execute runs a code review and returns the result.
// It selects single-model or consensus mode based on ComparisonModels config,
// validates the output, and optionally saves review artifacts.
func (e *Executor) Execute(ctx context.Context, params Params) (*Result, error) {
	startTime := time.Now()

	if params.Diff == "" {
		raw := "No code changes."
		if e.config.ReviewsDir != "" {
			ts := time.Now().Format("20060102-150405")
			result := ragReview.ComparisonResult{
				Model:    "review",
				Review:   raw,
				Duration: time.Since(startTime),
			}
			ragReview.SaveReviewArtifact(e.config.Logger, e.config.ReviewsDir, result, params.Event, ts)
		}

		return &Result{
			Review: &core.StructuredReview{
				Summary:     "This pull request contains no code changes. Looks good to me!",
				Suggestions: []core.Suggestion{},
			},
			RawReview: raw,
			DiffHash:  hashDiff(""),
		}, nil
	}

	diffHash := hashDiff(params.Diff)

	var structuredReview *core.StructuredReview
	var rawReview string
	var err error

	if len(e.config.ComparisonModels) > 0 {
		e.config.Logger.Info("using consensus review", "models", e.config.ComparisonModels)
		structuredReview, rawReview, err = e.ragService.GenerateConsensusReview(
			ctx,
			params.RepoConfig,
			params.Repo,
			params.Event,
			e.config.ComparisonModels,
			params.Diff,
			params.ChangedFiles,
		)
	} else {
		e.config.Logger.Info("using single-model review")
		structuredReview, rawReview, err = e.ragService.GenerateReview(
			ctx,
			params.RepoConfig,
			params.Repo,
			params.Event,
			params.Diff,
			params.ChangedFiles,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("review generation failed: %w", err)
	}

	// Validate result
	if structuredReview == nil || (structuredReview.Summary == "" && len(structuredReview.Suggestions) == 0) {
		e.config.Logger.Error("generated review is empty or invalid", "raw_review", rawReview)
		return nil, errors.New("generated review is empty or invalid")
	}

	// Save review artifact if configured
	if e.config.ReviewsDir != "" && rawReview != "" {
		ts := time.Now().Format("20060102-150405")
		result := ragReview.ComparisonResult{
			Model:    "review",
			Review:   rawReview,
			Duration: time.Since(startTime),
		}
		ragReview.SaveReviewArtifact(e.config.Logger, e.config.ReviewsDir, result, params.Event, ts)
	}

	e.config.Logger.Info("review completed",
		"verdict", structuredReview.Verdict,
		"confidence", structuredReview.Confidence,
		"suggestions", len(structuredReview.Suggestions),
		"diff_hash", diffHash,
	)

	res := &Result{
		Review:    structuredReview,
		RawReview: rawReview,
		DiffHash:  diffHash,
	}

	if len(e.config.ComparisonModels) > 0 {
		res.ModelsUsed = e.config.ComparisonModels
	}

	return res, nil
}

// hashDiff creates a SHA-256 hex hash of the diff for tracking changes.
func hashDiff(diff string) string {
	h := sha256.Sum256([]byte(diff))
	return hex.EncodeToString(h[:])
}
