package contextpkg

import (
	"log/slog"
	"testing"

	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/sevigo/code-warden/mocks"
)

// TestGetArchContextForPaths_ExactFilter verifies that arch summary lookup uses
// an exact source+chunk_type filter rather than a similarity scan.
func TestGetArchContextForPaths_ExactFilter(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockScopedVectorStore(ctrl)

	// Only one SimilaritySearch call expected — for "internal/rag" directory.
	// The call must carry filters; we capture them to assert their content.
	var capturedOpts []any
	mockStore.EXPECT().
		SimilaritySearch(gomock.Any(), "internal/rag", 1, gomock.Any()).
		DoAndReturn(func(_ any, _ string, _ int, opts ...any) ([]schema.Document, error) {
			capturedOpts = opts
			return []schema.Document{
				{
					PageContent: "RAG pipeline: retrieval-augmented generation for code review",
					Metadata: map[string]any{
						"source":     "internal/rag",
						"chunk_type": "arch",
					},
				},
			}, nil
		})

	b := &builderImpl{cfg: Config{Logger: slog.Default()}}

	result, err := b.GetArchContextForPaths(t.Context(), mockStore, []string{"internal/rag/service.go"})

	require.NoError(t, err)
	assert.NotEmpty(t, capturedOpts, "expected filter options to be passed")
	assert.Contains(t, result, "internal/rag")
	assert.Contains(t, result, "RAG pipeline")
}

// TestGetArchContextForPaths_MissingDir verifies that a directory with no stored
// arch summary produces an empty result without error.
func TestGetArchContextForPaths_MissingDir(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockScopedVectorStore(ctrl)
	mockStore.EXPECT().
		SimilaritySearch(gomock.Any(), gomock.Any(), 1, gomock.Any()).
		Return([]schema.Document{}, nil)

	b := &builderImpl{cfg: Config{Logger: slog.Default()}}

	result, err := b.GetArchContextForPaths(t.Context(), mockStore, []string{"some/new/dir/file.go"})

	require.NoError(t, err)
	assert.Empty(t, result)
}

// TestGetArchContextForPaths_DeduplicatesDirs verifies that multiple files in
// the same directory produce only one arch summary lookup.
func TestGetArchContextForPaths_DeduplicatesDirs(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockScopedVectorStore(ctrl)
	// Must be called exactly once even though two files share the same dir.
	mockStore.EXPECT().
		SimilaritySearch(gomock.Any(), "internal/rag", 1, gomock.Any()).
		Return([]schema.Document{
			{PageContent: "summary", Metadata: map[string]any{"source": "internal/rag"}},
		}, nil).
		Times(1)

	b := &builderImpl{cfg: Config{Logger: slog.Default()}}

	_, err := b.GetArchContextForPaths(t.Context(), mockStore, []string{
		"internal/rag/service.go",
		"internal/rag/context.go",
	})
	require.NoError(t, err)
}

// TestGetArchContextForPaths_EmptyPaths verifies that an empty path list
// returns empty result without hitting the store.
func TestGetArchContextForPaths_EmptyPaths(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockScopedVectorStore(ctrl)
	// No store calls expected.

	b := &builderImpl{cfg: Config{Logger: slog.Default()}}

	result, err := b.GetArchContextForPaths(t.Context(), mockStore, []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}
