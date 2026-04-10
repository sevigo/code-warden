package core

import (
	"testing"

	"github.com/google/go-github/v73/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventFromPushEvent_DefaultBranch(t *testing.T) {
	event := &github.PushEvent{
		Ref: github.Ptr("refs/heads/main"),
		Repo: &github.PushEventRepository{
			Owner: &github.PushEventRepoOwner{
				Login: github.Ptr("acme"),
			},
			Name:          github.Ptr("myrepo"),
			FullName:      github.Ptr("acme/myrepo"),
			CloneURL:      github.Ptr("https://github.com/acme/myrepo.git"),
			Language:      github.Ptr("Go"),
			DefaultBranch:  github.Ptr("main"),
		},
		Installation: &github.Installation{
			ID: github.Ptr(int64(42)),
		},
		After: github.Ptr("abc123def456"),
	}

	got, err := EventFromPushEvent(event)
	require.NoError(t, err)
	assert.Equal(t, ReIndex, got.Type)
	assert.Equal(t, "acme", got.RepoOwner)
	assert.Equal(t, "myrepo", got.RepoName)
	assert.Equal(t, "acme/myrepo", got.RepoFullName)
	assert.Equal(t, "https://github.com/acme/myrepo.git", got.RepoCloneURL)
	assert.Equal(t, "Go", got.Language)
	assert.Equal(t, int64(42), got.InstallationID)
	assert.Equal(t, "abc123def456", got.HeadSHA)
}

func TestEventFromPushEvent_NonDefaultBranch(t *testing.T) {
	event := &github.PushEvent{
		Ref: github.Ptr("refs/heads/feature-branch"),
		Repo: &github.PushEventRepository{
			Owner: &github.PushEventRepoOwner{
				Login: github.Ptr("acme"),
			},
			Name:          github.Ptr("myrepo"),
			FullName:      github.Ptr("acme/myrepo"),
			DefaultBranch:  github.Ptr("main"),
		},
		Installation: &github.Installation{
			ID: github.Ptr(int64(42)),
		},
	}

	_, err := EventFromPushEvent(event)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not to the default branch")
}

func TestEventFromPushEvent_MasterDefaultBranch(t *testing.T) {
	event := &github.PushEvent{
		Ref: github.Ptr("refs/heads/master"),
		Repo: &github.PushEventRepository{
			Owner: &github.PushEventRepoOwner{
				Login: github.Ptr("acme"),
			},
			Name:          github.Ptr("myrepo"),
			FullName:      github.Ptr("acme/myrepo"),
			CloneURL:      github.Ptr("https://github.com/acme/myrepo.git"),
			DefaultBranch:  github.Ptr("master"),
		},
		Installation: &github.Installation{
			ID: github.Ptr(int64(7)),
		},
		After: github.Ptr("deadbeef"),
	}

	got, err := EventFromPushEvent(event)
	require.NoError(t, err)
	assert.Equal(t, ReIndex, got.Type)
	assert.Equal(t, "deadbeef", got.HeadSHA)
}

func TestEventFromPushEvent_EmptyDefaultBranchFallsBackToMain(t *testing.T) {
	event := &github.PushEvent{
		Ref: github.Ptr("refs/heads/main"),
		Repo: &github.PushEventRepository{
			Owner: &github.PushEventRepoOwner{
				Login: github.Ptr("acme"),
			},
			Name:          github.Ptr("myrepo"),
			FullName:      github.Ptr("acme/myrepo"),
			CloneURL:      github.Ptr("https://github.com/acme/myrepo.git"),
			DefaultBranch:  github.Ptr(""), // empty
		},
		Installation: &github.Installation{
			ID: github.Ptr(int64(1)),
		},
		After: github.Ptr("cafe1234"),
	}

	got, err := EventFromPushEvent(event)
	require.NoError(t, err)
	assert.Equal(t, ReIndex, got.Type)
}

func TestEventFromPushEvent_MissingRepo(t *testing.T) {
	event := &github.PushEvent{
		Ref:  github.Ptr("refs/heads/main"),
		Repo: nil,
	}

	_, err := EventFromPushEvent(event)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repository or owner information is missing")
}

func TestEventFromPushEvent_MissingOwner(t *testing.T) {
	event := &github.PushEvent{
		Ref: github.Ptr("refs/heads/main"),
		Repo: &github.PushEventRepository{
			Owner:    nil,
			Name:     github.Ptr("myrepo"),
			FullName: github.Ptr("acme/myrepo"),
		},
	}

	_, err := EventFromPushEvent(event)
	assert.Error(t, err)
}

func TestEventFromPushEvent_MissingInstallation(t *testing.T) {
	event := &github.PushEvent{
		Ref: github.Ptr("refs/heads/main"),
		Repo: &github.PushEventRepository{
			Owner: &github.PushEventRepoOwner{
				Login: github.Ptr("acme"),
			},
			Name:          github.Ptr("myrepo"),
			FullName:      github.Ptr("acme/myrepo"),
			DefaultBranch:  github.Ptr("main"),
		},
		Installation: nil,
	}

	_, err := EventFromPushEvent(event)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "installation ID is missing")
}