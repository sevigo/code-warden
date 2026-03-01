package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/google/go-github/v73/github"
	githubclient "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/mocks"
	"go.uber.org/mock/gomock"
)

func TestCreatePRTool_Execute(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGH := mocks.NewMockClient(ctrl)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	tool := NewCreatePRTool(mockGH, "owner", "repo", logger)

	ctx := context.Background()

	t.Run("successful PR creation", func(t *testing.T) {
		args := map[string]any{
			"title": "Test PR",
			"body":  "Test body",
			"head":  "feature",
			"base":  "main",
		}

		mockGH.EXPECT().
			GetBranch(ctx, "owner", "repo", "main").
			Return(&github.Branch{}, nil)

		mockGH.EXPECT().
			CreatePullRequest(ctx, "owner", "repo", githubclient.PullRequestOptions{
				Title: "Test PR",
				Body:  "Test body",
				Head:  "feature",
				Base:  "main",
			}).
			Return(&github.PullRequest{
				Number:  github.Ptr(123),
				HTMLURL: github.Ptr("https://github.com/pr/123"),
				State:   github.Ptr("open"),
			}, nil)

		result, err := tool.Execute(ctx, args)
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}

		got, ok := result.(CreatePRResponse)
		if !ok {
			t.Fatalf("expected CreatePRResponse, got %T", result)
		}
		if got.Number != 123 || got.URL != "https://github.com/pr/123" {
			t.Errorf("got %+v, want number 123 and URL https://github.com/pr/123", got)
		}
	})

	t.Run("missing required args", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]any{"title": "Missing body"})
		if err == nil {
			t.Error("expected error for missing body, got nil")
		}
	})

	t.Run("body too long", func(t *testing.T) {
		longBody := make([]byte, maxBodyLength+1)
		for i := range longBody {
			longBody[i] = 'a'
		}
		_, err := tool.Execute(ctx, map[string]any{
			"title": "Title",
			"body":  string(longBody),
			"head":  "feature",
		})
		if err == nil {
			t.Error("expected error for body too long, got nil")
		}
	})

	t.Run("base branch does not exist", func(t *testing.T) {
		args := map[string]any{
			"title": "Test PR",
			"body":  "Test body",
			"head":  "feature",
			"base":  "nonexistent",
		}

		mockGH.EXPECT().
			GetBranch(ctx, "owner", "repo", "nonexistent").
			Return(nil, fmt.Errorf("not found"))

		_, err := tool.Execute(ctx, args)
		if err == nil {
			t.Error("expected error for nonexistent base branch, got nil")
		}
	})
}

func TestListIssuesTool_Execute(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGH := mocks.NewMockClient(ctrl)
	tool := NewListIssuesTool(mockGH, "owner", "repo", slog.Default())

	ctx := context.Background()

	t.Run("list open issues", func(t *testing.T) {
		mockGH.EXPECT().
			ListIssues(ctx, "owner", "repo", githubclient.IssueOptions{
				State: "open",
				Limit: 30,
			}).
			Return([]githubclient.Issue{
				{Number: 1, Title: "Issue 1"},
				{Number: 2, Title: "Issue 2"},
			}, nil)

		result, err := tool.Execute(ctx, map[string]any{})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}

		got, ok := result.(ListIssuesResponse)
		if !ok {
			t.Fatalf("expected ListIssuesResponse, got %T", result)
		}
		if got.Count != 2 || got.Issues[0].Number != 1 {
			t.Errorf("got %+v, want count 2 and first issue #1", got)
		}
	})
}

func TestGetIssueTool_Execute(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGH := mocks.NewMockClient(ctrl)
	tool := NewGetIssueTool(mockGH, "owner", "repo", slog.Default())

	ctx := context.Background()

	t.Run("get issue by number", func(t *testing.T) {
		mockGH.EXPECT().
			GetIssue(ctx, "owner", "repo", 42).
			Return(&githubclient.Issue{Number: 42, Title: "The Answer"}, nil)

		result, err := tool.Execute(ctx, map[string]any{"number": 42.0})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}

		got := result.(*githubclient.Issue)
		if got.Number != 42 || got.Title != "The Answer" {
			t.Errorf("got %+v, want issue #42", got)
		}
	})
}
