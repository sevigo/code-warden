package reviewcli

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

// GitDiff runs `git diff` in the repo at repoPath and returns the unified diff
// string. base selects what to diff against:
//   - if base is empty or "HEAD", it diffs the working tree against HEAD
//     (i.e. uncommitted changes).
//   - otherwise it diffs base...HEAD (changes on the current branch since base).
func GitDiff(ctx context.Context, repoPath, base string) (string, error) {
	args := []string{"diff"}
	if base != "" && base != "HEAD" {
		args = append(args, base+"...HEAD")
	} else {
		args = append(args, "HEAD")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff in %q: %w: %s", repoPath, err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// ChangedFilesFromDiff splits a unified diff string into per-file entries.
func ChangedFilesFromDiff(diff string) []internalgithub.ChangedFile {
	return agentreview.ParseDiff(diff)
}

// GitLog returns the commit messages for the current branch since base (or all
// recent commits when base is empty). It is used to give the review agent
// context on the intent of the changes.
func GitLog(ctx context.Context, repoPath, base string) ([]string, error) {
	args := []string{"log", "--pretty=format:%s"}
	if base != "" && base != "HEAD" {
		args = append(args, base+"..HEAD")
	} else {
		args = append(args, "-10")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log in %q: %w: %s", repoPath, err, strings.TrimSpace(errb.String()))
	}
	var msgs []string
	for _, line := range strings.Split(out.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			msgs = append(msgs, line)
		}
	}
	return msgs, nil
}
