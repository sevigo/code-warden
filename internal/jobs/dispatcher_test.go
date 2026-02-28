package jobs

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
)

type mockJob struct {
	processCh  chan struct{}
	delay      time.Duration
	blockUntil chan struct{}
}

func newMockJob() *mockJob {
	return &mockJob{
		processCh: make(chan struct{}, 100),
	}
}

func (m *mockJob) Run(ctx context.Context, event *core.GitHubEvent) error {
	if m.blockUntil != nil {
		<-m.blockUntil
	}
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	select {
	case m.processCh <- struct{}{}:
	default:
	}
	return nil
}

func TestDispatcher_QueueFull_ReturnsErrQueueFull(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	blockUntil := make(chan struct{})
	mockReviewJob := &mockJob{blockUntil: blockUntil}

	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxWorkers: 1,
		},
	}

	d := NewDispatcher(context.Background(), mockReviewJob, cfg, logger)
	defer func() {
		close(blockUntil)
		d.Stop()
	}()

	queueCapacity := 100
	var dispatchedCount, droppedCount int

	for i := 0; i < queueCapacity+20; i++ {
		event := &core.GitHubEvent{
			RepoFullName: "test/repo",
			PRNumber:     i,
		}
		err := d.Dispatch(context.Background(), event)
		if err == nil {
			dispatchedCount++
		} else if errors.Is(err, ErrQueueFull) {
			droppedCount++
		}
	}

	if droppedCount < 1 {
		t.Errorf("expected at least 1 job to be dropped, got %d", droppedCount)
	}

	dispatcher := d.(*dispatcher)
	metrics := dispatcher.Metrics()

	if metrics.JobsDropped != int64(droppedCount) {
		t.Errorf("expected %d jobs dropped in metrics, got %d", droppedCount, metrics.JobsDropped)
	}
}

func TestDispatcher_Metrics_TracksCounters(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mockReviewJob := newMockJob()

	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxWorkers: 1,
		},
	}

	d := NewDispatcher(context.Background(), mockReviewJob, cfg, logger)

	event := &core.GitHubEvent{
		RepoFullName: "test/repo",
		PRNumber:     1,
	}
	_ = d.Dispatch(context.Background(), event)

	select {
	case <-mockReviewJob.processCh:
	case <-time.After(2 * time.Second):
		t.Fatal("job was not processed within timeout")
	}

	d.Stop()

	dispatcher := d.(*dispatcher)
	metrics := dispatcher.Metrics()

	if metrics.JobsDispatched != 1 {
		t.Errorf("expected 1 job dispatched, got %d", metrics.JobsDispatched)
	}
	if metrics.JobsProcessed != 1 {
		t.Errorf("expected 1 job processed, got %d", metrics.JobsProcessed)
	}
	if metrics.QueueCapacity != 100 {
		t.Errorf("expected queue capacity 100, got %d", metrics.QueueCapacity)
	}
}

func TestDispatcher_Metrics_TracksDroppedJobs(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	blockUntil := make(chan struct{})
	mockReviewJob := &mockJob{blockUntil: blockUntil}

	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxWorkers: 1,
		},
	}

	d := NewDispatcher(context.Background(), mockReviewJob, cfg, logger)

	queueCapacity := 100
	droppedCount := 0

	for i := 0; i < queueCapacity+20; i++ {
		event := &core.GitHubEvent{
			RepoFullName: "test/repo",
			PRNumber:     i,
		}
		err := d.Dispatch(context.Background(), event)
		if errors.Is(err, ErrQueueFull) {
			droppedCount++
		}
	}

	dispatcher := d.(*dispatcher)
	metrics := dispatcher.Metrics()

	if metrics.JobsDropped != int64(droppedCount) {
		t.Errorf("expected %d jobs dropped, got %d", droppedCount, metrics.JobsDropped)
	}

	close(blockUntil)
	d.Stop()
}

func TestDispatcher_Metrics_QueueDepth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	startProcessing := make(chan struct{})
	processDone := make(chan struct{})

	mockReviewJob := &mockJobWithControl{
		startProcessing: startProcessing,
		processDone:     processDone,
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxWorkers: 1,
		},
	}

	d := NewDispatcher(context.Background(), mockReviewJob, cfg, logger)

	for i := 0; i < 5; i++ {
		event := &core.GitHubEvent{
			RepoFullName: "test/repo",
			PRNumber:     i,
		}
		_ = d.Dispatch(context.Background(), event)
	}

	dispatcher := d.(*dispatcher)
	metrics := dispatcher.Metrics()

	if metrics.QueueDepth < 0 || metrics.QueueDepth > 5 {
		t.Errorf("queue depth should be between 0-5, got %d", metrics.QueueDepth)
	}

	startProcessing <- struct{}{}
	<-processDone
	d.Stop()
}

type mockJobWithControl struct {
	startProcessing chan struct{}
	processDone     chan struct{}
}

func (m *mockJobWithControl) Run(ctx context.Context, event *core.GitHubEvent) error {
	<-m.startProcessing
	m.processDone <- struct{}{}
	return nil
}

func TestDispatcher_ErrQueueFull_IsSentinelError(t *testing.T) {
	if !errors.Is(ErrQueueFull, ErrQueueFull) {
		t.Error("ErrQueueFull should match itself via errors.Is")
	}

	wrapped := errors.Join(ErrQueueFull, errors.New("additional context"))
	if !errors.Is(wrapped, ErrQueueFull) {
		t.Error("ErrQueueFull should be detectable even when wrapped")
	}
}

func TestDispatcher_ConcurrentDispatch(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	blockUntil := make(chan struct{})
	mockReviewJob := &mockJob{blockUntil: blockUntil}

	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxWorkers: 4,
		},
	}

	d := NewDispatcher(context.Background(), mockReviewJob, cfg, logger)

	var wg sync.WaitGroup
	var dispatched, dropped atomic.Int64

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			event := &core.GitHubEvent{
				RepoFullName: "test/repo",
				PRNumber:     id,
			}
			err := d.Dispatch(context.Background(), event)
			if err == nil {
				dispatched.Add(1)
			} else if errors.Is(err, ErrQueueFull) {
				dropped.Add(1)
			}
		}(i)
	}

	wg.Wait()

	dispatcher := d.(*dispatcher)
	metrics := dispatcher.Metrics()

	total := dispatched.Load() + dropped.Load()
	if total != 200 {
		t.Errorf("expected 200 total dispatches, got %d", total)
	}

	if metrics.JobsDropped != dropped.Load() {
		t.Errorf("metrics.JobsDropped (%d) should match dropped counter (%d)", metrics.JobsDropped, dropped.Load())
	}

	close(blockUntil)
	d.Stop()
}
