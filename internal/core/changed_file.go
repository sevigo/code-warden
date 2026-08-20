package core

// ChangedFile is one file in a review diff together with its unified-diff
// patch. It is independent of the integration that supplied the change.
type ChangedFile struct {
	Filename string
	Patch    string
}
