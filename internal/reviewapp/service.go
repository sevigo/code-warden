// Package reviewapp defines the provider-neutral application boundary for code
// reviews. Integrations load a ReviewInput, the Service runs the shared engine,
// and integration-specific reporters decide how to present the ReviewResult.
package reviewapp

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sevigo/goframe/llms"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/llm"
)

// ReviewInput is all provider-neutral data needed to review one change.
type ReviewInput struct {
	Repository     string
	Diff           string
	ChangedFiles   []core.ChangedFile
	CommitMessages []string
	WorkspaceDir   string
	CloneURL       string
}

// ReviewOptions controls execution and finding filters for one review.
type ReviewOptions struct {
	Timeout       time.Duration
	MaxIterations int
	ContextWindow int
	Config        *agentreview.Config
}

// ReviewResult is the provider-neutral outcome of a review.
type ReviewResult struct {
	Review *core.StructuredReview
	Raw    string
	Angles []agentreview.AngleResult
}

// ReviewSource loads review input from an integration such as a local checkout
// or a hosted pull request.
type ReviewSource interface {
	Load(context.Context) (ReviewInput, error)
}

// Reviewer is the application-level review operation used by integrations.
type Reviewer interface {
	Review(context.Context, ReviewInput, ReviewOptions) (*ReviewResult, error)
}

type reviewRunner interface {
	Run(context.Context, agentreview.Params) (*agentreview.Result, error)
}

// Service runs the shared review engine independently of its input source and
// output destination.
type Service struct {
	runner reviewRunner
}

// NewService creates a review service using the standard multi-angle runner.
func NewService(model llms.Model, promptMgr *llm.PromptManager, tools agentreview.ToolBuilder, logger *slog.Logger) *Service {
	executor := agentreview.NewGoframeAngleExecutor(model, promptMgr, tools, logger)
	return &Service{runner: agentreview.NewRunner(executor, logger, nil)}
}

// Review executes one provider-neutral review request.
func (s *Service) Review(ctx context.Context, input ReviewInput, opts ReviewOptions) (*ReviewResult, error) {
	result, err := s.runner.Run(ctx, agentreview.Params{
		Diff:           input.Diff,
		ChangedFiles:   input.ChangedFiles,
		RepoURL:        input.CloneURL,
		WorkspaceDir:   input.WorkspaceDir,
		RepoFullName:   input.Repository,
		CommitMessages: input.CommitMessages,
		Timeout:        opts.Timeout,
		MaxIterations:  opts.MaxIterations,
		ContextWindow:  opts.ContextWindow,
		Config:         opts.Config,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("review runner returned no result")
	}
	return &ReviewResult{Review: result.Review, Raw: result.Raw, Angles: result.Angles}, nil
}

var _ Reviewer = (*Service)(nil)
