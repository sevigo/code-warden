// Package reviewcli contains review sources and terminal presentation helpers
// used by the standalone review command.
package reviewcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/reviewapp"
)

// LocalSource loads a review from a local Git checkout.
type LocalSource struct {
	path string
	base string
}

// NewLocalSource creates a local-checkout review source.
func NewLocalSource(path, base string) *LocalSource {
	return &LocalSource{path: path, base: base}
}

// Load resolves the checkout, calculates its diff, and returns neutral review
// input. Commit messages are best-effort context and do not block a review.
func (s *LocalSource) Load(ctx context.Context) (reviewapp.ReviewInput, error) {
	abs, err := filepath.Abs(s.path)
	if err != nil {
		return reviewapp.ReviewInput{}, fmt.Errorf("resolve local checkout: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return reviewapp.ReviewInput{}, fmt.Errorf("not a directory: %s", abs)
	}

	diff, err := GitDiff(ctx, abs, s.base)
	if err != nil {
		return reviewapp.ReviewInput{}, err
	}
	commits, _ := GitLog(ctx, abs, s.base)
	return reviewapp.ReviewInput{
		Repository:     filepath.Base(abs),
		Diff:           diff,
		ChangedFiles:   ChangedFilesFromDiff(diff),
		CommitMessages: commits,
		WorkspaceDir:   abs,
	}, nil
}

// PRSource loads a pull request directly through GitHub's public API. It is
// used by the standalone CLI and does not require a GitHub App installation.
type PRSource struct {
	input   PRInput
	cleanup func()
}

// NewPRSource creates a standalone GitHub pull-request source.
func NewPRSource(input PRInput) *PRSource {
	return &PRSource{input: input}
}

// Load fetches the pull request and checks out its exact head commit into a
// temporary workspace, so the review investigates what will actually be
// merged rather than the base repository's default branch. The caller must
// call Close once the review is done to remove the workspace.
func (s *PRSource) Load(ctx context.Context) (reviewapp.ReviewInput, error) {
	data, err := FetchPR(ctx, s.input)
	if err != nil {
		return reviewapp.ReviewInput{}, err
	}

	client := gitutil.NewClient(nil)
	workspace, cleanup, err := client.ClonePRHeadTemp(ctx, data.CloneURL, s.input.Number, data.HeadSHA, s.input.Token)
	if err != nil {
		return reviewapp.ReviewInput{}, fmt.Errorf("checkout PR head: %w", err)
	}
	s.cleanup = cleanup

	return reviewapp.ReviewInput{
		Repository:     s.input.Owner + "/" + s.input.Repo,
		Diff:           data.Diff,
		ChangedFiles:   data.ChangedFiles,
		CommitMessages: data.CommitMessages,
		WorkspaceDir:   workspace,
	}, nil
}

// Close removes the temporary head-commit workspace, if one was created.
func (s *PRSource) Close() {
	if s.cleanup != nil {
		s.cleanup()
	}
}

var (
	_ reviewapp.ReviewSource = (*LocalSource)(nil)
	_ reviewapp.ReviewSource = (*PRSource)(nil)
)
