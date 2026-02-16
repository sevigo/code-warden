package github_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/mocks"
)

func TestPostStructuredReview(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockClient(ctrl)
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	updater := github.NewStatusUpdater(mockClient, logger)

	review := &core.StructuredReview{
		Title:   "Test Review",
		Verdict: "REQUEST_CHANGES",
		Suggestions: []core.Suggestion{
			{
				FilePath:   "file1.go",
				LineNumber: 10,
				Severity:   "High",
				Comment:    "Issue 1",
			},
			{
				FilePath:   "file2.go",
				LineNumber: 20,
				Severity:   "Medium",
				Comment:    "Issue 2",
			},
		},
	}

	event := &core.GitHubEvent{
		RepoOwner: "owner",
		RepoName:  "repo",
		PRNumber:  123,
		HeadSHA:   "sha123",
	}

	// Expect CreateReview to be called with 2 comments
	mockClient.EXPECT().CreateReview(
		gomock.Any(),
		"owner",
		"repo",
		123,
		"sha123",
		gomock.Any(), // Summary body
		gomock.AssignableToTypeOf([]github.DraftReviewComment{}),
	).DoAndReturn(func(_ context.Context, _ string, _ string, _ int, _ string, _ string, comments []github.DraftReviewComment) error {
		assert.Len(t, comments, 2)
		assert.Equal(t, "file1.go", comments[0].Path)
		assert.Equal(t, 10, comments[0].Line)
		assert.Equal(t, "file2.go", comments[1].Path)
		assert.Equal(t, 20, comments[1].Line)
		return nil
	})

	err := updater.PostStructuredReview(context.Background(), event, review)
	assert.NoError(t, err)
}
