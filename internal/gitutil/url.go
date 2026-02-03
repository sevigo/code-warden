package gitutil

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var prURLRegex = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)$`)

// ParsePullRequestURL parses a GitHub Pull Request URL and extracts the owner, repo, and PR number.
// Supported format: https://github.com/{owner}/{repo}/pull/{number}
func ParsePullRequestURL(url string) (owner, repo string, prNumber int, err error) {
	// Normalize URL
	url = strings.TrimSuffix(url, "/")

	matches := prURLRegex.FindStringSubmatch(url)
	if len(matches) != 4 {
		return "", "", 0, fmt.Errorf("invalid pull request URL format: %s", url)
	}

	owner = matches[1]
	repo = matches[2]
	prNumberStr := matches[3]

	prNumber, err = strconv.Atoi(prNumberStr)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid PR number '%s': %w", prNumberStr, err)
	}

	return owner, repo, prNumber, nil
}
