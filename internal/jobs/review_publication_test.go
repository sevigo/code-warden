package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/code-warden/mocks"
)

type recordingStatusUpdater struct {
	postErr        error
	posted         int
	completed      int
	postedContents []*core.StructuredReview
}

func (u *recordingStatusUpdater) InProgress(context.Context, *core.GitHubEvent, string, string) (int64, error) {
	return 0, nil
}

func (u *recordingStatusUpdater) Completed(context.Context, *core.GitHubEvent, int64, string, string, string) error {
	u.completed++
	return nil
}

func (u *recordingStatusUpdater) PostStructuredReview(_ context.Context, _ *core.GitHubEvent, review *core.StructuredReview) error {
	u.posted++
	u.postedContents = append(u.postedContents, review)
	return u.postErr
}

func (u *recordingStatusUpdater) PostSimpleComment(context.Context, *core.GitHubEvent, string) error {
	return nil
}

var _ github.StatusUpdater = (*recordingStatusUpdater)(nil)

func TestCompleteReviewPublishesAndMarksRecord(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	job := &ReviewJob{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	updater := &recordingStatusUpdater{}
	event := publicationTestEvent()
	review := publicationTestReview()

	store.EXPECT().SaveReview(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, saved *core.Review) error {
		saved.ID = 17
		require.Equal(t, storage.ReviewPublicationPending, saved.PublicationStatus)
		require.Contains(t, saved.ReviewContent, "<suggestion>")
		return nil
	})
	store.EXPECT().UpdateReviewPublicationStatus(gomock.Any(), int64(17), storage.ReviewPublicationPublished).Return(nil)

	err := job.completeReview(context.Background(), event, &reviewEnvironment{statusUpdater: updater, checkRunID: 9}, review, publicationValidLines())
	require.NoError(t, err)
	require.Equal(t, 1, updater.posted)
	require.Equal(t, 1, updater.completed)
}

func TestCompleteReviewLeavesFailedPublicationPending(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	job := &ReviewJob{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	updater := &recordingStatusUpdater{postErr: errors.New("github unavailable")}
	event := publicationTestEvent()

	store.EXPECT().SaveReview(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, saved *core.Review) error {
		saved.ID = 18
		return nil
	})

	err := job.completeReview(context.Background(), event, &reviewEnvironment{statusUpdater: updater}, publicationTestReview(), publicationValidLines())
	require.ErrorContains(t, err, "failed to post review comment to GitHub")
	require.Equal(t, 1, updater.posted)
	require.Equal(t, 0, updater.completed)
}

func TestCompleteReviewRetriesPendingRecord(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	job := &ReviewJob{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	updater := &recordingStatusUpdater{}
	event := publicationTestEvent()

	store.EXPECT().SaveReview(gomock.Any(), gomock.Any()).Return(storage.ErrDuplicateReview)
	store.EXPECT().GetLatestReviewForPR(gomock.Any(), event.RepoFullName, event.PRNumber).Return(&core.Review{
		ID:                19,
		HeadSHA:           event.HeadSHA,
		PublicationStatus: storage.ReviewPublicationPending,
	}, nil)
	store.EXPECT().UpdateReviewPublicationStatus(gomock.Any(), int64(19), storage.ReviewPublicationPublished).Return(nil)

	err := job.completeReview(context.Background(), event, &reviewEnvironment{statusUpdater: updater}, publicationTestReview(), publicationValidLines())
	require.NoError(t, err)
	require.Equal(t, 1, updater.posted)
}

func TestCompleteReviewSkipsPublishedRecord(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	job := &ReviewJob{store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	updater := &recordingStatusUpdater{}
	event := publicationTestEvent()

	store.EXPECT().SaveReview(gomock.Any(), gomock.Any()).Return(storage.ErrDuplicateReview)
	store.EXPECT().GetLatestReviewForPR(gomock.Any(), event.RepoFullName, event.PRNumber).Return(&core.Review{
		ID:                20,
		HeadSHA:           event.HeadSHA,
		PublicationStatus: storage.ReviewPublicationPublished,
	}, nil)

	err := job.completeReview(context.Background(), event, &reviewEnvironment{statusUpdater: updater, checkRunID: 10}, publicationTestReview(), publicationValidLines())
	require.NoError(t, err)
	require.Equal(t, 0, updater.posted)
	require.Equal(t, 1, updater.completed)
}

func publicationTestEvent() *core.GitHubEvent {
	return &core.GitHubEvent{
		RepoFullName: "owner/repo",
		RepoOwner:    "owner",
		RepoName:     "repo",
		PRNumber:     12,
		HeadSHA:      "abc123",
	}
}

func publicationTestReview() *core.StructuredReview {
	return &core.StructuredReview{
		Summary: "One issue found.",
		Verdict: core.VerdictRequestChanges,
		Suggestions: []core.Suggestion{{
			FilePath:   "main.go",
			LineNumber: 7,
			Severity:   "High",
			Category:   "Bug",
			Comment:    "Handle the returned error.",
		}},
	}
}

func publicationValidLines() map[string]map[int]struct{} {
	return map[string]map[int]struct{}{"main.go": {7: {}}}
}
