package core

// UpdateResult contains the lists of files that have changed between two commits.
type UpdateResult struct {
	FilesToAddOrUpdate []string
	FilesToDelete      []string
	RepoPath           string
	IsInitialClone     bool
}
