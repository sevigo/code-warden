package repomanager

import "errors"

var (
	ErrRepoNotFound      = errors.New("git repository not found on disk")
	ErrRepoNameDetection = errors.New("cannot detect repo name from remotes")
)
