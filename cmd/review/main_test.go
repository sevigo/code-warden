package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/reviewcli"
)

func TestBuildReviewRequestSelectsLocalSource(t *testing.T) {
	t.Parallel()

	reviewConfig := agentreview.DefaultConfig()
	source, opts, err := buildReviewRequest(".", "", 0, "", "main", time.Minute, 4, 32000, &reviewConfig)

	require.NoError(t, err)
	assert.IsType(t, &reviewcli.LocalSource{}, source)
	assert.Equal(t, time.Minute, opts.Timeout)
	assert.Equal(t, 4, opts.MaxIterations)
	assert.Equal(t, 32000, opts.ContextWindow)
	assert.Same(t, &reviewConfig, opts.Config)
}

func TestBuildReviewRequestSelectsPRSource(t *testing.T) {
	t.Parallel()

	source, _, err := buildReviewRequest("", "owner/repo", 7, "token", "", 0, 0, 0, nil)

	require.NoError(t, err)
	assert.IsType(t, &reviewcli.PRSource{}, source)
}

func TestBuildReviewRequestRejectsInvalidPRName(t *testing.T) {
	t.Parallel()

	source, _, err := buildReviewRequest("", "invalid", 7, "", "", 0, 0, 0, nil)

	require.ErrorContains(t, err, "expected owner/repo")
	assert.Nil(t, source)
}

func TestPresentReviewPreservesJSONAndPromptModes(t *testing.T) {
	t.Parallel()

	review := &core.StructuredReview{
		Verdict: core.VerdictRequestChanges,
		Summary: "one issue",
		Suggestions: []core.Suggestion{{
			FilePath:   "main.go",
			LineNumber: 7,
			Severity:   "High",
			Category:   "Bug",
			Comment:    "unsafe behavior",
		}},
	}

	var jsonOutput bytes.Buffer
	exitCode, err := presentReview(&jsonOutput, review, true, false)
	require.NoError(t, err)
	assert.Equal(t, 1, exitCode)
	assert.Contains(t, jsonOutput.String(), `"verdict":"REQUEST_CHANGES"`)

	var promptOutput bytes.Buffer
	exitCode, err = presentReview(&promptOutput, review, false, true)
	require.NoError(t, err)
	assert.Equal(t, 1, exitCode)
	assert.Contains(t, promptOutput.String(), "VERDICT: REQUEST_CHANGES")
	assert.Contains(t, promptOutput.String(), "File: main.go:7")
}
