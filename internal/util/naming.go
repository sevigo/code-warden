package util

import (
	"fmt"
	"regexp"
	"strings"
)

var collectionNameRegexp = regexp.MustCompile("[^a-z0-9_-]+")

// GenerateCollectionName builds a valid vector DB collection name from repo and model info.
func GenerateCollectionName(repoFullName, embedderName string) string {
	safeRepoName := strings.ToLower(strings.ReplaceAll(repoFullName, "/", "-"))
	safeEmbedderName := strings.ToLower(strings.Split(embedderName, ":")[0])

	safeRepoName = collectionNameRegexp.ReplaceAllString(safeRepoName, "")
	safeEmbedderName = collectionNameRegexp.ReplaceAllString(safeEmbedderName, "")

	collectionName := fmt.Sprintf("repo-%s-%s", safeRepoName, safeEmbedderName)

	const maxCollectionNameLength = 255
	if len(collectionName) > maxCollectionNameLength {
		collectionName = collectionName[:maxCollectionNameLength]
	}
	return collectionName
}
