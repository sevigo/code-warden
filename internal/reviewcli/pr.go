package reviewcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

// PRInput describes a public GitHub pull request to review without a GitHub App.
type PRInput struct {
	Owner  string
	Repo   string
	Number int
	// Token is an optional GitHub token for private repos or higher rate limits.
	// Public repos work without one.
	Token string
}

// PRData is the result of fetching PR info: the diff, the changed files, a
// clone URL that the runner can use to investigate the PR's state, and the
// commit messages describing the changes.
type PRData struct {
	Diff           string
	ChangedFiles   []internalgithub.ChangedFile
	CloneURL       string
	CommitMessages []string
}

// apiBase is the GitHub REST API base URL. Variable (not const) so tests can
// point it at a local httptest server.
var apiBase = "https://api.github.com"

// FetchPR fetches the diff and changed files for a public (or token-authed) PR.
// It does not require a GitHub App installation.
func FetchPR(ctx context.Context, in PRInput) (*PRData, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	// 1. Pull request metadata (head repo clone URL).
	prURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, in.Owner, in.Repo, in.Number)
	prBytes, err := getBody(ctx, client, prURL, in.Token, "")
	if err != nil {
		return nil, err
	}
	var pr struct {
		Head struct {
			SHA  string `json:"sha"`
			Repo struct {
				CloneURL string `json:"clone_url"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := json.Unmarshal(prBytes, &pr); err != nil {
		return nil, fmt.Errorf("decode PR metadata: %w", err)
	}
	cloneURL := pr.Head.Repo.CloneURL
	if cloneURL == "" {
		return nil, fmt.Errorf("PR head repo has no clone URL")
	}

	// 2. PR diff (unified diff via the .diff endpoint).
	diffURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, in.Owner, in.Repo, in.Number)
	diffBytes, err := getBody(ctx, client, diffURL+".diff", in.Token, "application/vnd.github.v3.diff")
	if err != nil {
		return nil, err
	}
	diff := string(diffBytes)
	if diff == "" {
		return nil, fmt.Errorf("PR %d has no diff", in.Number)
	}

	// 3. Commit messages (best-effort; non-fatal if unavailable).
	commitsURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/commits", apiBase, in.Owner, in.Repo, in.Number)
	var commitMessages []string
	if commitsBytes, err := getBody(ctx, client, commitsURL, in.Token, ""); err == nil {
		var commits []struct {
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		}
		if json.Unmarshal(commitsBytes, &commits) == nil {
			for _, c := range commits {
				if m := strings.TrimSpace(c.Commit.Message); m != "" {
					commitMessages = append(commitMessages, m)
				}
			}
		}
	}

	return &PRData{
		Diff:           diff,
		ChangedFiles:   agentreview.ParseDiff(diff),
		CloneURL:       cloneURL,
		CommitMessages: commitMessages,
	}, nil
}

// getBody performs an authenticated GET and returns the response body.
// accept overrides the Accept header; when empty, the default GitHub JSON
// format is requested.
func getBody(ctx context.Context, client *http.Client, rawURL, token, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GET %s: status %d: %s", rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}

// embedTokenInCloneURL injects a token into an https clone URL so the pure-Go
// cloner can authenticate for private repos.
func embedTokenInCloneURL(cloneURL, token string) string {
	if token == "" || !strings.HasPrefix(cloneURL, "https://") {
		return cloneURL
	}
	u, err := url.Parse(cloneURL)
	if err != nil {
		return cloneURL
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}
