package core

import (
	"testing"

	"github.com/google/go-github/v73/github"
	"github.com/stretchr/testify/assert"
)

func TestEventFromPushEvent(t *testing.T) {
	t.Run("valid push to default branch", func(t *testing.T) {
		event := &github.PushEvent{
			Ref:  github.Ptr("refs/heads/main"),
			Head: github.Ptr("abc123def456"),
			Repo: &github.PushEventRepository{
				FullName:      github.Ptr("owner/repo"),
				CloneURL:      github.Ptr("https://github.com/owner/repo.git"),
				Language:      github.Ptr("Go"),
				DefaultBranch: github.Ptr("main"),
				Owner: &github.User{
					Login: github.Ptr("owner"),
				},
				Name: github.Ptr("repo"),
			},
			Installation: &github.Installation{
				ID: github.Ptr(int64(12345)),
			},
			Sender: &github.User{
				Login: github.Ptr("someuser"),
			},
		}

		got, err := EventFromPushEvent(event)
		assert.NoError(t, err)
		assert.Equal(t, Reindex, got.Type)
		assert.Equal(t, "owner", got.RepoOwner)
		assert.Equal(t, "repo", got.RepoName)
		assert.Equal(t, "owner/repo", got.RepoFullName)
		assert.Equal(t, "https://github.com/owner/repo.git", got.RepoCloneURL)
		assert.Equal(t, "Go", got.Language)
		assert.Equal(t, int64(12345), got.InstallationID)
		assert.Equal(t, "abc123def456", got.HeadSHA)
		assert.Equal(t, "someuser", got.Commenter)
	})

	t.Run("push to feature branch is rejected", func(t *testing.T) {
		event := &github.PushEvent{
			Ref: github.Ptr("refs/heads/feature-branch"),
			Repo: &github.PushEventRepository{
				FullName:      github.Ptr("owner/repo"),
				DefaultBranch: github.Ptr("main"),
				Owner: &github.User{
					Login: github.Ptr("owner"),
				},
				Name: github.Ptr("repo"),
			},
			Installation: &github.Installation{
				ID: github.Ptr(int64(12345)),
			},
		}

		got, err := EventFromPushEvent(event)
		assert.Nil(t, got)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "non-default branch")
	})

	t.Run("nil event returns error", func(t *testing.T) {
		got, err := EventFromPushEvent(nil)
		assert.Nil(t, got)
		assert.Error(t, err)
	})

	t.Run("nil repo returns error", func(t *testing.T) {
		got, err := EventFromPushEvent(&github.PushEvent{})
		assert.Nil(t, got)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "repo is nil")
	})

	t.Run("missing owner returns error", func(t *testing.T) {
		event := &github.PushEvent{
			Repo: &github.PushEventRepository{
				FullName:      github.Ptr("owner/repo"),
				DefaultBranch: github.Ptr("main"),
				Name:          github.Ptr("repo"),
			},
			Installation: &github.Installation{
				ID: github.Ptr(int64(12345)),
			},
		}

		got, err := EventFromPushEvent(event)
		assert.Nil(t, got)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "owner information is missing")
	})

	t.Run("missing installation ID returns error", func(t *testing.T) {
		event := &github.PushEvent{
			Ref: github.Ptr("refs/heads/main"),
			Repo: &github.PushEventRepository{
				FullName:      github.Ptr("owner/repo"),
				DefaultBranch: github.Ptr("main"),
				Owner: &github.User{
					Login: github.Ptr("owner"),
				},
				Name: github.Ptr("repo"),
			},
		}

		got, err := EventFromPushEvent(event)
		assert.Nil(t, got)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "installation ID is missing")
	})

	t.Run("empty ref returns error", func(t *testing.T) {
		event := &github.PushEvent{
			Ref: github.Ptr(""),
			Repo: &github.PushEventRepository{
				FullName:      github.Ptr("owner/repo"),
				DefaultBranch: github.Ptr("main"),
				Owner: &github.User{
					Login: github.Ptr("owner"),
				},
				Name: github.Ptr("repo"),
			},
			Installation: &github.Installation{
				ID: github.Ptr(int64(12345)),
			},
		}

		got, err := EventFromPushEvent(event)
		assert.Nil(t, got)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ref or default branch is empty")
	})

	t.Run("different default branch name", func(t *testing.T) {
		event := &github.PushEvent{
			Ref:  github.Ptr("refs/heads/master"),
			Head: github.Ptr("deadbeef"),
			Repo: &github.PushEventRepository{
				FullName:      github.Ptr("owner/repo"),
				CloneURL:      github.Ptr("https://github.com/owner/repo.git"),
				Language:      github.Ptr("Python"),
				DefaultBranch: github.Ptr("master"),
				Owner: &github.User{
					Login: github.Ptr("owner"),
				},
				Name: github.Ptr("repo"),
			},
			Installation: &github.Installation{
				ID: github.Ptr(int64(99999)),
			},
			Sender: &github.User{
				Login: github.Ptr("deploy-bot"),
			},
		}

		got, err := EventFromPushEvent(event)
		assert.NoError(t, err)
		assert.Equal(t, Reindex, got.Type)
		assert.Equal(t, "deadbeef", got.HeadSHA)
		assert.Equal(t, "deploy-bot", got.Commenter)
	})
}
