package repomanager

import "errors"

var (
	ErrRepoNameDetection = errors.New("cannot detect repo name from remotes")
)
