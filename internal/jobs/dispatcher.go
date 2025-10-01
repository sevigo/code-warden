// Package jobs defines background tasks such as automated code reviews.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sevigo/code-warden/internal/core"
)

type jobPayload struct {
	ctx   context.Context
	event *core.GitHubEvent
}

// dispatcher implements core.JobDispatcher and manages a pool of worker goroutines
// for processing GitHub events as code review jobs.
type dispatcher struct {
	reviewJob  core.Job
	jobQueue   chan *jobPayload
	maxWorkers int
	wg         sync.WaitGroup
	logger     *slog.Logger
	mainCtx    context.Context
}

// NewDispatcher initializes a dispatcher with a worker pool.
// If maxWorkers is 0 or negative, it defaults to 1.
func NewDispatcher(ctx context.Context, reviewJob core.Job, maxWorkers int, logger *slog.Logger) core.JobDispatcher {
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	d := &dispatcher{
		reviewJob:  reviewJob,
		maxWorkers: maxWorkers,
		jobQueue:   make(chan *jobPayload, 100),
		logger:     logger,
		mainCtx:    ctx,
	}
	d.startWorkers()
	return d
}

// startWorkers launches maxWorkers goroutines to process jobs from the queue.
func (d *dispatcher) startWorkers() {
	for i := range d.maxWorkers {
		d.wg.Add(1)
		go d.startWorker(i)
	}
}

// startWorker processes events from the queue until it's closed.
func (d *dispatcher) startWorker(workerID int) {
	defer d.wg.Done()
	d.logger.Info("starting review worker", "id", workerID)

	for payload := range d.jobQueue {
		d.processEvent(payload.ctx, workerID, payload.event)
	}

	d.logger.Info("shutting down review worker", "id", workerID)
}

// processEvent logs and runs a review job for a GitHub event.
func (d *dispatcher) processEvent(ctx context.Context, workerID int, event *core.GitHubEvent) {
	d.logger.Info("worker processing job",
		"worker_id", workerID,
		"repo", event.RepoFullName,
	)

	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("panic recovered in review job", "panic", r, "repo", event.RepoFullName)
			// TODO: update the GitHub check run to "failed" here.
		}
	}()

	if err := d.reviewJob.Run(ctx, event); err != nil {
		d.logger.Error("code review job failed",
			"repo", event.RepoFullName,
			"pr", event.PRNumber,
			"error", err,
		)
	}
}

// Dispatch queues a GitHub event for processing by a worker.
func (d *dispatcher) Dispatch(ctx context.Context, event *core.GitHubEvent) error {
	d.logger.Info("queuing code review job", "repo", event.RepoFullName, "pr", event.PRNumber)

	jobCtx := d.mainCtx

	select {
	case d.jobQueue <- &jobPayload{ctx: jobCtx, event: event}:
		return nil
	default:
		return fmt.Errorf("job queue is full, cannot accept new review job")
	}
}

// Stop gracefully shuts down the dispatcher, waiting for all workers to finish.
func (d *dispatcher) Stop() {
	d.logger.Info("stopping dispatcher and waiting for jobs to finish")
	close(d.jobQueue)
	d.wg.Wait()
	d.logger.Info("all review jobs have finished")
}
