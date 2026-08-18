package review

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
)

func TestDeduplicate(t *testing.T) {
	reviews := []*core.StructuredReview{
		{
			Suggestions: []core.Suggestion{
				{FilePath: "a.go", LineNumber: 10, Severity: "Medium", Comment: "first"},
				{FilePath: "b.go", LineNumber: 5, Severity: "Low", Comment: "second"},
			},
		},
		{
			Suggestions: []core.Suggestion{
				// Same location as the first review's first finding, higher severity.
				{FilePath: "a.go", LineNumber: 10, Severity: "Critical", Comment: "dup higher"},
				{FilePath: "c.go", LineNumber: 7, Severity: "Medium", Comment: "third"},
			},
		},
	}

	got := Deduplicate(reviews)
	require.Len(t, got, 3)

	byKey := map[string]core.Suggestion{}
	for _, s := range got {
		byKey[findingKey(s)] = s
	}
	// a.go:10 should have kept the higher severity.
	require.Equal(t, "Critical", byKey["a.go:10"].Severity)
	require.Equal(t, "dup higher", byKey["a.go:10"].Comment)
	require.Contains(t, byKey, "b.go:5")
	require.Contains(t, byKey, "c.go:7")
}

func TestDeduplicateTiesKeepFirst(t *testing.T) {
	reviews := []*core.StructuredReview{
		{Suggestions: []core.Suggestion{
			{FilePath: "a.go", LineNumber: 1, Severity: "High", Comment: "first-pass"},
		}},
		{Suggestions: []core.Suggestion{
			{FilePath: "a.go", LineNumber: 1, Severity: "High", Comment: "second-pass"},
		}},
	}
	got := Deduplicate(reviews)
	require.Len(t, got, 1)
	require.Equal(t, "first-pass", got[0].Comment)
}
