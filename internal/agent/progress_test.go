package agent

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

// TestProgressTracker_RecordConcurrency verifies that concurrent record() calls
// do not race or panic. Run with -race to detect data races.
func TestProgressTracker_RecordConcurrency(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracker := newProgressTracker(
		&Session{ID: "test-session"},
		&buf,
		"precise (LSP)",
		func(_ context.Context, _ string) int64 { return 1 },
		func(_ context.Context, _ int64, _ string) {},
	)

	const goroutines = 20
	const callsPerGoroutine = 50

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(_ int) {
			defer wg.Done()
			for j := range callsPerGoroutine {
				tracker.record("test_tool", j%2 == 0)
			}
		}(i)
	}
	wg.Wait()

	tracker.mu.Lock()
	got := len(tracker.entries)
	tracker.mu.Unlock()

	want := goroutines * callsPerGoroutine
	if got != want {
		t.Errorf("expected %d entries, got %d", want, got)
	}
}

// TestProgressTracker_PhaseChange verifies setPhase is reflected in entries.
func TestProgressTracker_PhaseChange(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracker := newProgressTracker(
		&Session{ID: "phase-test"},
		&buf,
		"degraded (RAG-only)",
		func(_ context.Context, _ string) int64 { return 0 },
		func(_ context.Context, _ int64, _ string) {},
	)

	tracker.setPhase("planning")
	tracker.record("read_file", true)
	tracker.setPhase("implementing")
	tracker.record("write_file", true)

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(tracker.entries))
	}
	if tracker.entries[0].Phase != "planning" {
		t.Errorf("first entry phase = %q, want planning", tracker.entries[0].Phase)
	}
	if tracker.entries[1].Phase != "implementing" {
		t.Errorf("second entry phase = %q, want implementing", tracker.entries[1].Phase)
	}
}

// TestProgressTracker_CommentEditsNotCreates verifies that after the first comment
// is created (ID > 0), subsequent ticks call updateComment not createComment.
func TestProgressTracker_CommentEditsNotCreates(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	creates := 0
	updates := 0
	var mu sync.Mutex

	tracker := newProgressTracker(
		&Session{ID: "edit-test"},
		&buf,
		"precise (LSP)",
		func(_ context.Context, _ string) int64 {
			mu.Lock()
			creates++
			mu.Unlock()
			return 42 // non-zero = success
		},
		func(_ context.Context, _ int64, _ string) {
			mu.Lock()
			updates++
			mu.Unlock()
		},
	)

	ctx := context.Background()
	tracker.start(ctx)

	// Record calls to trigger maybePostComment.
	for range 3 {
		tracker.record("tool_x", true)
		tracker.maybePostComment(ctx)
		time.Sleep(10 * time.Millisecond) // let goroutine finish
	}

	tracker.stop()

	mu.Lock()
	c, u := creates, updates
	mu.Unlock()

	if c != 1 {
		t.Errorf("expected 1 create call, got %d", c)
	}
	if u < 1 {
		t.Errorf("expected at least 1 update call, got %d", u)
	}
}

// TestCompactionFilterText verifies exploration tools are stripped.
func TestCompactionFilterText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input     string
		wantStrip bool
	}{
		// goframe formats tool results as "Tool '<name>' returned: <json>"
		{"Tool 'read_file' returned: {\"content\": \"...\"}", true},
		{"Tool 'search_code' returned: {\"results\": []}", true},
		{"Tool 'list_dir' returned: {\"entries\": []}", true},
		{"Tool 'grep' returned: {\"matches\": []}", true},
		{"Tool 'find' returned: {\"files\": []}", true},
		{"Tool 'get_symbol' returned: {\"name\": \"foo\"}", true},
		{"Tool 'write_file' returned: {\"ok\": true}", false},
		{"Tool 'edit_file' returned: {\"ok\": true}", false},
		{"Tool 'review_code' returned: {\"verdict\": \"APPROVE\"}", false},
		{"Tool 'run_command' returned: {\"output\": \"ok\"}", false},
		{"some random assistant message", false},
		// Coordinate LSP tools were removed from the explorer set;
		// lsp_diagnostics is also not stripped (it belongs to verification).
		{"Tool 'lsp_diagnostics' returned: {\"ok\": true}", false},
	}
	for _, tc := range cases {
		t.Run(tc.input[:min(20, len(tc.input))], func(t *testing.T) {
			t.Parallel()
			out := compactionFilterText(tc.input)
			stripped := out != tc.input
			if stripped != tc.wantStrip {
				t.Errorf("compactionFilterText(%q) stripped=%v want=%v (output=%q)",
					tc.input, stripped, tc.wantStrip, out)
			}
		})
	}
}
