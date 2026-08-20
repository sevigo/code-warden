package reviewapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/core"
)

func TestServiceReviewMapsInputAndOptions(t *testing.T) {
	t.Parallel()

	wantReview := &core.StructuredReview{Summary: "done"}
	runner := &recordingRunner{result: &agentreview.Result{Review: wantReview, Raw: "raw review"}}
	service := &Service{runner: runner}
	reviewConfig := agentreview.DefaultConfig()
	input := ReviewInput{
		Repository:     "owner/repo",
		Diff:           "diff content",
		ChangedFiles:   []core.ChangedFile{{Filename: "main.go", Patch: "@@ -1 +1 @@"}},
		CommitMessages: []string{"fix boundary"},
		WorkspaceDir:   "workspace",
		CloneURL:       "https://example.test/repo.git",
	}
	opts := ReviewOptions{
		Timeout:       2 * time.Minute,
		MaxIterations: 12,
		ContextWindow: 64000,
		Config:        &reviewConfig,
	}

	result, err := service.Review(context.Background(), input, opts)

	require.NoError(t, err)
	assert.Same(t, wantReview, result.Review)
	assert.Equal(t, "raw review", result.Raw)
	assert.Equal(t, input.Diff, runner.params.Diff)
	assert.Equal(t, input.ChangedFiles, runner.params.ChangedFiles)
	assert.Equal(t, input.CloneURL, runner.params.RepoURL)
	assert.Equal(t, input.WorkspaceDir, runner.params.WorkspaceDir)
	assert.Equal(t, input.Repository, runner.params.RepoFullName)
	assert.Equal(t, input.CommitMessages, runner.params.CommitMessages)
	assert.Equal(t, opts.Timeout, runner.params.Timeout)
	assert.Equal(t, opts.MaxIterations, runner.params.MaxIterations)
	assert.Equal(t, opts.ContextWindow, runner.params.ContextWindow)
	assert.Same(t, opts.Config, runner.params.Config)
}

func TestServiceReviewReturnsRunnerError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("review failed")
	service := &Service{runner: &recordingRunner{err: wantErr}}

	result, err := service.Review(context.Background(), ReviewInput{}, ReviewOptions{})

	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, result)
}

func TestServiceReviewRejectsNilRunnerResult(t *testing.T) {
	t.Parallel()

	service := &Service{runner: &recordingRunner{}}

	result, err := service.Review(context.Background(), ReviewInput{}, ReviewOptions{})

	require.ErrorContains(t, err, "review runner returned no result")
	assert.Nil(t, result)
}

type recordingRunner struct {
	params agentreview.Params
	result *agentreview.Result
	err    error
}

func (r *recordingRunner) Run(_ context.Context, params agentreview.Params) (*agentreview.Result, error) {
	r.params = params
	return r.result, r.err
}
