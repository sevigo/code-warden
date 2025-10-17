package repomanager

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-git/go-git/v5"
)

// getRepoFullName extracts “owner/repo” from any remote URL (HTTPS or SSH).
func (m *manager) getRepoFullName(repo *git.Repository) (string, error) {
	remotes, err := repo.Remotes()
	if err != nil {
		return "", fmt.Errorf("remotes: %w", err)
	}
	for _, r := range remotes {
		if len(r.Config().URLs) == 0 {
			continue
		}
		if name, ok := parseRemoteURL(r.Config().URLs[0]); ok {
			return name, nil
		}
	}
	return "", ErrRepoNameDetection
}

func parseRemoteURL(raw string) (string, bool) {
	// HTTPS – https://github.com/owner/repo.git
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"), true
	}
	// SSH – git@github.com:owner/repo.git
	if strings.Contains(raw, "@") && strings.Contains(raw, ":") {
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) == 2 {
			return strings.TrimSuffix(parts[1], ".git"), true
		}
	}
	return "", false
}

func GenerateCollectionName(repoFullName, embedderName string) string {
	safeRepo := strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))
	safeEmbed := strings.ToLower(strings.Split(embedderName, ":")[0])

	safeRepo = collectionNameRegexp.ReplaceAllString(safeRepo, "")
	safeEmbed = collectionNameRegexp.ReplaceAllString(safeEmbed, "")

	name := "repo-" + safeRepo + "-" + safeEmbed
	if len(name) > maxCollectionNameLength {
		return name[:maxCollectionNameLength]
	}
	return name
}
