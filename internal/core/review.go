package core

import "time"

// Review represents a single code review stored in the database.
type Review struct {
	ID            int64
	RepoFullName  string
	PRNumber      int
	HeadSHA       string
	ReviewContent string
	CreatedAt     time.Time
}
