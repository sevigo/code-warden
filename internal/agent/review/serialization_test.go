package review

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
)

func TestMarshalStructuredReviewRoundTrip(t *testing.T) {
	t.Parallel()

	want := &core.StructuredReview{
		Summary:    "Found an escaped value: a < b & c > d.",
		Verdict:    core.VerdictRequestChanges,
		Confidence: 91,
		Suggestions: []core.Suggestion{{
			FilePath:   "internal/example.go",
			LineNumber: 42,
			Severity:   "High",
			Category:   "Bug",
			Comment:    "The result is discarded.",
		}},
	}

	raw, err := MarshalStructuredReview(want)
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	require.Contains(t, raw, "<review>")
	require.Contains(t, raw, "<severity>High</severity>")

	got, err := NewStructuredReviewParser(slog.Default()).Parse(context.Background(), raw)
	require.NoError(t, err)
	require.Equal(t, want.Summary, got.Summary)
	require.Equal(t, want.Verdict, got.Verdict)
	require.Equal(t, want.Suggestions, got.Suggestions)
}

func TestMarshalStructuredReviewRejectsNil(t *testing.T) {
	t.Parallel()

	_, err := MarshalStructuredReview(nil)
	require.Error(t, err)
}
