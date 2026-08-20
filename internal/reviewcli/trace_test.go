package reviewcli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/reviewapp"
)

func TestWriteTracePersistsSafeReviewArtifacts(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 20, 12, 30, 0, 0, time.UTC)
	reviewConfig := agentreview.DefaultConfig()
	request := TraceRequest{
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(1500 * time.Millisecond),
		Model: TraceModel{
			Provider:        "ollama",
			Model:           "qwen3:8b",
			ThinkingEnabled: true,
			ThinkingEffort:  "medium",
		},
		Input: reviewapp.ReviewInput{
			Repository:     "local/example",
			Diff:           "diff --git a/main.go b/main.go\n+var answer = 42\n",
			ChangedFiles:   []core.ChangedFile{{Filename: "main.go"}},
			CommitMessages: []string{"fix calculation"},
			WorkspaceDir:   "/private/workspace",
			CloneURL:       "https://x-access-token:secret-token@example.test/repo.git",
		},
		Options: reviewapp.ReviewOptions{
			Timeout:       2 * time.Minute,
			MaxIterations: 12,
			ContextWindow: 64000,
			Config:        &reviewConfig,
		},
		Result: &reviewapp.ReviewResult{
			Review: &core.StructuredReview{
				Summary: "one issue",
				Verdict: core.VerdictRequestChanges,
				Suggestions: []core.Suggestion{{
					FilePath: "main.go", LineNumber: 2, Severity: "high", Comment: "bad value",
				}},
			},
			Raw: "<review><summary>one issue</summary></review>",
			Angles: []agentreview.AngleResult{{
				Angle:       "Bug / Correctness",
				Status:      agentreview.AngleStatusPartial,
				Iterations:  12,
				TokensIn:    1200,
				TokensOut:   180,
				Raw:         "raw model response",
				Suggestions: []core.Suggestion{{FilePath: "main.go", LineNumber: 2}},
			}},
		},
	}

	runDir, err := WriteTrace(t.TempDir(), request)
	require.NoError(t, err)
	assert.Contains(t, filepath.Base(runDir), "20260820T123000Z-")

	manifest := readTraceManifest(t, runDir)
	assert.Equal(t, traceSchemaVersion, manifest.SchemaVersion)
	assert.Equal(t, "completed", manifest.Status)
	assert.Equal(t, int64(1500), manifest.DurationMillis)
	assert.Equal(t, "local/example", manifest.Repository)
	assert.Equal(t, "qwen3:8b", manifest.Model.Model)
	assert.Equal(t, int64(120000), manifest.Options.AngleTimeoutMillis)
	assert.Equal(t, []string{"main.go"}, manifest.ChangedFiles)
	assert.Equal(t, "input.diff", manifest.DiffFile)
	assert.Equal(t, "review.json", manifest.ReviewJSONFile)
	assert.Equal(t, "review.xml", manifest.ReviewXMLFile)
	require.Len(t, manifest.Angles, 1)
	assert.Equal(t, "angle-01-bug-correctness.raw.txt", manifest.Angles[0].RawFile)
	assert.Equal(t, agentreview.AngleStatusPartial, manifest.Angles[0].Status)

	assertFileContent(t, runDir, "input.diff", request.Input.Diff)
	assertFileContent(t, runDir, manifest.Angles[0].RawFile, "raw model response")
	assertFileContent(t, runDir, "review.xml", request.Result.Raw)

	entries, err := os.ReadDir(runDir)
	require.NoError(t, err)
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(runDir, entry.Name()))
		require.NoError(t, readErr)
		assert.NotContains(t, string(data), "secret-token")
		assert.NotContains(t, string(data), "/private/workspace")
		info, statErr := entry.Info()
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestWriteTracePersistsFailedRun(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 20, 13, 0, 0, 0, time.UTC)
	runDir, err := WriteTrace(t.TempDir(), TraceRequest{
		StartedAt:  startedAt,
		FinishedAt: startedAt.Add(time.Second),
		Input: reviewapp.ReviewInput{
			Repository: "local/failure",
			Diff:       "broken diff",
		},
		Err: errors.New("model unavailable"),
	})
	require.NoError(t, err)

	manifest := readTraceManifest(t, runDir)
	assert.Equal(t, "failed", manifest.Status)
	assert.Equal(t, "model unavailable", manifest.Error)
	assert.Empty(t, manifest.ReviewJSONFile)
	assert.Empty(t, manifest.Angles)
	assert.Equal(t, agentreview.DefaultAngleTimeout.Milliseconds(), manifest.Options.AngleTimeoutMillis)
	assert.Equal(t, agentreview.DefaultMaxIterations, manifest.Options.MaxIterations)
	assert.Equal(t, agentreview.DefaultContextWindow, manifest.Options.ContextWindow)
	assert.NoFileExists(t, filepath.Join(runDir, "review.json"))
}

func TestWriteTraceRequiresRoot(t *testing.T) {
	t.Parallel()

	path, err := WriteTrace(" ", TraceRequest{})
	require.ErrorContains(t, err, "trace directory is required")
	assert.Empty(t, path)
}

func readTraceManifest(t *testing.T, runDir string) traceManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	require.NoError(t, err)
	var manifest traceManifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	return manifest
}

func assertFileContent(t *testing.T, runDir, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, name))
	require.NoError(t, err)
	assert.Equal(t, want, string(data))
}
