package repomanager

import "regexp"

const (
	maxCollectionNameLength = 255
)

var collectionNameRegexp = regexp.MustCompile("[^a-z0-9_-]+")
