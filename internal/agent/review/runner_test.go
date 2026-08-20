package review

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
)

func TestRunnerDelegatesAnglesAndCollectsExecutionResults(t *testing.T) {
	t.Parallel()

	executor := &recordingAngleExecutor{
		execute: func(request AngleRequest) (AngleResult, error) {
			line := 2
			severity := "high"
			status := AngleStatusCompleted
			if request.Angle.Name == "conventions" {
				line = 3
				severity = "medium"
				status = AngleStatusPartial
			}
			return AngleResult{
				Angle: request.Angle.Name,
				Suggestions: []core.Suggestion{{
					FilePath:   "main.go",
					LineNumber: line,
					Severity:   severity,
					Comment:    request.Angle.Name + " finding",
				}},
				Raw:        "raw " + request.Angle.Name,
				Iterations: 2,
				TokensIn:   100,
				TokensOut:  20,
				Status:     status,
			}, nil
		},
	}
	angles := []Angle{
		{Name: "bug", Description: "correctness"},
		{Name: "conventions", Description: "maintainability"},
	}
	runner := NewRunner(executor, nil, angles)
	diff, changedFiles := runnerTestDiff()

	result, err := runner.Run(context.Background(), Params{
		Diff:           diff,
		ChangedFiles:   changedFiles,
		WorkspaceDir:   t.TempDir(),
		RepoFullName:   "local/example",
		MaxIterations:  12,
		ContextWindow:  64000,
		CommitMessages: []string{"fix local review"},
		Config:         &Config{MinSeverity: "low"},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, core.VerdictRequestChanges, result.Review.Verdict)
	assert.Len(t, result.Review.Suggestions, 2)
	assert.Len(t, result.Angles, 2)
	assert.NotEmpty(t, result.Raw)
	require.NotNil(t, result.Coverage)
	assert.Equal(t, CoverageStatusPartial, result.Coverage.Status)
	require.Len(t, result.Coverage.Files, 1)
	assert.Equal(t, CoverageItemReviewed, result.Coverage.Files[0].Status)
	require.Len(t, result.Coverage.Angles, 2)
	assert.Equal(t, CoverageItemCompleted, result.Coverage.Angles[0].Status)
	assert.Equal(t, CoverageItemPartial, result.Coverage.Angles[1].Status)

	requests := executor.Requests()
	require.Len(t, requests, 2)
	for _, request := range requests {
		assert.Equal(t, 12, request.MaxIterations)
		assert.Equal(t, 64000, request.ContextWindow)
		assert.Equal(t, diff, request.Diff)
		assert.Contains(t, request.TaskContext, "fix local review")
		assert.Contains(t, request.ChangedLines, "main.go")
	}
	assert.ElementsMatch(t, []string{"bug", "conventions"}, resultAngleNames(result.Angles))
}

func TestRunnerReportsAngleExecutorFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("model unavailable")
	executor := &recordingAngleExecutor{
		execute: func(AngleRequest) (AngleResult, error) {
			return AngleResult{}, wantErr
		},
	}
	runner := NewRunner(executor, nil, []Angle{{Name: "bug"}})
	diff, changedFiles := runnerTestDiff()

	result, err := runner.Run(context.Background(), Params{
		Diff:         diff,
		ChangedFiles: changedFiles,
		WorkspaceDir: t.TempDir(),
		Config:       &Config{MinSeverity: "low"},
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "quorum not achievable")
	assert.Nil(t, result)
	assert.Len(t, executor.Requests(), 1)
}

func TestRunnerRejectsMissingAngleExecutor(t *testing.T) {
	t.Parallel()

	runner := NewRunner(nil, nil, nil)
	result, err := runner.Run(context.Background(), Params{})

	require.ErrorContains(t, err, "angle executor is required")
	assert.Nil(t, result)
}

func TestRunnerReportsSkippedCoverageForDisabledAngles(t *testing.T) {
	t.Parallel()

	executor := &recordingAngleExecutor{execute: func(AngleRequest) (AngleResult, error) {
		return AngleResult{}, errors.New("executor must not run")
	}}
	runner := NewRunner(executor, nil, []Angle{{Name: "bug"}, {Name: "security"}})
	diff, changedFiles := runnerTestDiff()

	result, err := runner.Run(context.Background(), Params{
		Diff:         diff,
		ChangedFiles: changedFiles,
		WorkspaceDir: t.TempDir(),
		Config: &Config{
			MinSeverity:       "low",
			EnabledCategories: map[string]bool{"unknown": true},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, result.Coverage)
	assert.Equal(t, CoverageStatusSkipped, result.Coverage.Status)
	assert.Len(t, executor.Requests(), 0)
	assert.Equal(t, CoverageItemNotReviewed, result.Coverage.Files[0].Status)
	assert.Equal(t, CoverageItemSkipped, result.Coverage.Angles[0].Status)
	assert.Equal(t, "no review angles enabled", result.Coverage.Notes[0])
}

type recordingAngleExecutor struct {
	mu       sync.Mutex
	requests []AngleRequest
	execute  func(AngleRequest) (AngleResult, error)
}

func (e *recordingAngleExecutor) Execute(_ context.Context, request AngleRequest) (AngleResult, error) {
	e.mu.Lock()
	e.requests = append(e.requests, request)
	e.mu.Unlock()
	return e.execute(request)
}

func (e *recordingAngleExecutor) Requests() []AngleRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]AngleRequest(nil), e.requests...)
}

func resultAngleNames(results []AngleResult) []string {
	names := make([]string, 0, len(results))
	for _, result := range results {
		names = append(names, result.Angle)
	}
	return names
}

func runnerTestDiff() (string, []core.ChangedFile) {
	patch := "diff --git a/main.go b/main.go\n" +
		"--- a/main.go\n" +
		"+++ b/main.go\n" +
		"@@ -1 +1,3 @@\n" +
		" package main\n" +
		"+var first = 1\n" +
		"+var second = 2\n"
	return patch, []core.ChangedFile{{Filename: "main.go", Patch: patch}}
}

var _ AngleExecutor = (*recordingAngleExecutor)(nil)
