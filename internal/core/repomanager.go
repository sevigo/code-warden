// Package core defines the essential interfaces and data structures that form the
// backbone of the application. These components are designed to be abstract,
// allowing for flexible and decoupled implementations of the application's logic.
package core

// UpdateResult contains the lists of files that have changed between two commits
// during a repository sync operation.
type UpdateResult struct {
	// FilesToAddOrUpdate contains the paths of files that are new or have been modified.
	FilesToAddOrUpdate []string
	// FilesToDelete contains the paths of files that have been deleted.
	FilesToDelete []string
	// RepoPath is the local filesystem path where the repository is cloned.
	RepoPath string
	// RepoFullName is the full repository name in "owner/repo" format.
	RepoFullName string

	// HeadSHA is the PR's head SHA — used for logging and DB records only.
	HeadSHA string

	// DefaultBranchSHA is the SHA of the default branch (main) that was synced.
	// This is what gets persisted as LastIndexedSHA in the database.
	DefaultBranchSHA string

	// DefaultBranchChanged is true when the default branch advanced since the
	// last synced SHA.
	DefaultBranchChanged bool

	// IsInitialClone is true when this is the first sync of a repository.
	IsInitialClone bool
}
