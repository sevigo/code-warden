// Package core defines the essential interfaces and data structures that form the
// backbone of the application. These components are designed to be abstract,
// allowing for flexible and decoupled implementations of the application's logic.
package core

import (
	"context"
)

// JobDispatcher defines the contract for a system that can accept and queue
// background jobs for asynchronous processing. This interface decouples the
// event source (e.g., a webhook handler) from the job execution mechanism.
type JobDispatcher interface {
	// Dispatch accepts a GitHubEvent and queues it for processing.
	// It returns an error if the job cannot be queued, for example, if the
	// queue is full, providing a mechanism for backpressure.
	Dispatch(ctx context.Context, event *GitHubEvent) error
}

// Job represents a single, executable unit of work that can be processed by the
// application's job dispatcher. Each job is triggered by a GitHubEvent and
// performs a specific task, such as a code review.
type Job interface {
	// Run executes the job's logic. It receives a context for managing its
	// lifecycle and a GitHubEvent containing the data needed to perform its task.
	// It returns an error if the job fails to complete successfully.
	Run(ctx context.Context, event *GitHubEvent) error
}