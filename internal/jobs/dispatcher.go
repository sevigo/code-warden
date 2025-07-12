// Package jobs defines background tasks such as automated code reviews.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sevigo/code-warden/internal/core"
)

// dispatcher implements core.JobDispatcher and manages a pool of worker goroutines
// for processing GitHub events as code review jobs.
type dispatcher struct {
	reviewJob  core.Job               // Job implementation executed by each worker.
	jobQueue   chan *core.GitHubEvent // Queue of incoming GitHub events.
	maxWorkers int                    // Number of concurrent workers.
	wg         sync.WaitGroup         // Tracks active workers for graceful shutdown.
	logger     *slog.Logger           // Logger instance for the dispatcher.
}

// NewDispatcher initializes a dispatcher with a worker pool.
// If maxWorkers is 0 or negative, it defaults to 1.
func NewDispatcher(reviewJob core.Job, maxWorkers int, logger *slog.Logger) core.JobDispatcher {
	if maxWorkers <= 0 {
		maxWorkers = 1
	}
	d := &dispatcher{
		reviewJob:  reviewJob,
		maxWorkers: maxWorkers,
		jobQueue:   make(chan *core.GitHubEvent, 100),
		logger:     logger,
	}
	d.startWorkers()
	return d
}

// startWorkers launches maxWorkers goroutines to process jobs from the queue.
func (d *dispatcher) startWorkers() {
	for i := 0; i < d.maxWorkers; i++ {
		d.wg.Add(1)
		go func(workerID int) {
			defer d.wg.Done()
			d.logger.Info("starting review worker", "id", workerID)
			for event := range d.jobQueue {
				d.logger.Info("worker processing job", "worker_id", workerID, "repo", event.RepoFullName)
				if err := d.reviewJob.Run(context.Background(), event); err != nil {
					d.logger.Error("code review job failed", "repo", event.RepoFullName, "pr", event.PRNumber, "error", err)
				}
			}
			d.logger.Info("shutting down review worker", "id", workerID)
		}(i)
	}
}

// Dispatch queues a GitHub event for processing by a worker.
// Returns an error if the queue is full.
func (d *dispatcher) Dispatch(ctx context.Context, event *core.GitHubEvent) error {
	d.logger.InfoContext(ctx, "queuing code review job", "repo", event.RepoFullName, "pr", event.PRNumber)
	select {
	case d.jobQueue <- event:
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
