package core

// UpdateResult contains the lists of files that have changed between two commits.
type UpdateResult struct {
	FilesToAddOrUpdate []string
	FilesToDelete      []string
	RepoPath           string
	RepoFullName       string

	// HeadSHA is the PR's head SHA — used for logging and DB records only.
	// It is never written to Qdrant; only DefaultBranchSHA is indexed.
	HeadSHA string

	// DefaultBranchSHA is the SHA of the default branch (main) that was synced.
	// This is what gets persisted as LastIndexedSHA in the database.
	DefaultBranchSHA string

	// DefaultBranchChanged is true when the default branch advanced since the
	// last indexed SHA, meaning the Qdrant collection must be updated.
	DefaultBranchChanged bool

	IsInitialClone bool
}
