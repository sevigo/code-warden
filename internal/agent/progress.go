package agent

// progress.go — real-time progress tracking for agent sessions.
//
// progressTracker intercepts every tool call via the progressTool wrapper and:
//  1. Writes a timestamped line to the session log file immediately.
//  2. Maintains an in-memory list of recent calls.
//  3. Creates a single GitHub status comment on the first tick, then EDITs
//     that same comment every 30 seconds rather than posting new ones.
//
// This avoids cluttering the issue timeline with dozens of progress comments
// during long GLM-5.1 / MiniMax M2.7 runs.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"

	"github.com/sevigo/code-warden/internal/mcp"
)

// progressInterval is how often the background goroutine posts a GitHub update.
const progressInterval = 30 * time.Second

// progressRecentWindow is how many recent tool calls are shown in the comment.
const progressRecentWindow = 6

// progressEntry records a single tool execution.
type progressEntry struct {
	Phase   string
	Tool    string
	Success bool
	At      time.Time
}

// progressTracker tracks agent tool calls, writes them to the session log,
// and posts/edits a single GitHub progress comment.
type progressTracker struct {
	session *Session
	logW    io.Writer

	// createComment creates the initial status comment and returns its ID.
	// Returns 0 on error. Called at most once.
	createComment func(ctx context.Context, body string) int64
	// updateComment edits the comment created by createComment.
	// No-op when commentID is 0.
	updateComment func(ctx context.Context, id int64, body string)

	// lspMode is included in every status comment so the reader knows
	// whether precise LSP diagnostics or RAG-only mode is active.
	lspMode string

	mu              sync.Mutex
	entries         []progressEntry
	lastPosted      int // number of entries at the time of the last comment
	phase           string
	statusCommentID int64     // 0 until the first comment is created
	commentOnce     sync.Once // ensures createComment is called exactly once

	done chan struct{}
	wg   sync.WaitGroup
}

// newProgressTracker creates a tracker but does not start the background goroutine.
// lspMode should be "precise (LSP)" or "degraded (RAG-only)".
func newProgressTracker(
	session *Session,
	logW io.Writer,
	lspMode string,
	createComment func(ctx context.Context, body string) int64,
	updateComment func(ctx context.Context, id int64, body string),
) *progressTracker {
	return &progressTracker{
		session:       session,
		logW:          logW,
		lspMode:       lspMode,
		createComment: createComment,
		updateComment: updateComment,
		phase:         "implementing",
		done:          make(chan struct{}),
	}
}

// start launches the background goroutine that posts periodic GitHub comments.
func (pt *progressTracker) start(ctx context.Context) {
	pt.wg.Add(1)
	go func() {
		defer pt.wg.Done()
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()

		for {
			select {
			case <-pt.done:
				return
			case <-ticker.C:
				pt.maybePostComment(ctx)
			}
		}
	}()
}

// stop signals the background goroutine and waits for it to finish.
func (pt *progressTracker) stop() {
	close(pt.done)
	pt.wg.Wait()
}

// setPhase updates the current phase label shown in progress comments.
func (pt *progressTracker) setPhase(phase string) {
	pt.mu.Lock()
	pt.phase = phase
	pt.mu.Unlock()
}

// record logs a tool call. Called from progressTool.Execute after each tool runs.
// The log write is performed inside the mutex so concurrent calls do not race
// on the shared io.Writer.
func (pt *progressTracker) record(tool string, success bool) {
	status := "ok"
	if !success {
		status = "error"
	}

	pt.mu.Lock()
	phase := pt.phase
	entry := progressEntry{
		Phase:   phase,
		Tool:    tool,
		Success: success,
		At:      time.Now(),
	}
	pt.entries = append(pt.entries, entry)
	fmt.Fprintf(pt.logW, "[%s] [%s] TOOL %-25s %s\n",
		entry.At.Format("15:04:05"), strings.ToUpper(phase), tool, status)
	pt.mu.Unlock()
}

// maybePostComment creates or edits the GitHub status comment when new tool
// calls have occurred since the last update. Safe to call from any goroutine.
func (pt *progressTracker) maybePostComment(ctx context.Context) {
	pt.mu.Lock()
	total := len(pt.entries)
	if total == pt.lastPosted {
		pt.mu.Unlock()
		return
	}

	phase := pt.phase
	start := max(0, total-progressRecentWindow)
	recent := make([]progressEntry, total-start)
	copy(recent, pt.entries[start:])
	pt.lastPosted = total
	id := pt.statusCommentID
	pt.mu.Unlock()

	body := pt.buildCommentBody(phase, total, recent)

	// Post in a goroutine so a slow GitHub API call does not block the next tick.
	// commentOnce ensures createComment is called exactly once even when two ticks
	// fire back-to-back before the first goroutine's API call returns (race fix).
	go func(commentID int64, b string) {
		if commentID == 0 {
			pt.commentOnce.Do(func() {
				newID := pt.createComment(ctx, b)
				if newID != 0 {
					pt.mu.Lock()
					pt.statusCommentID = newID
					pt.mu.Unlock()
				}
			})
		} else {
			pt.updateComment(ctx, commentID, b)
		}
	}(id, body)
}

// buildCommentBody formats the GitHub progress comment.
func (pt *progressTracker) buildCommentBody(phase string, total int, recent []progressEntry) string {
	phaseLabel := map[string]string{
		"planning":     "Planning",
		"implementing": "Implementing",
		"publishing":   "Publishing",
	}[phase]
	if phaseLabel == "" {
		phaseLabel = phase
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🔄 **Implementation in progress** — session `%s`\n\n", pt.session.ID)
	fmt.Fprintf(&sb, "| | |\n|---|---|\n")
	fmt.Fprintf(&sb, "| **Phase** | %s |\n", phaseLabel)
	fmt.Fprintf(&sb, "| **Mode** | %s |\n", pt.lspMode)
	fmt.Fprintf(&sb, "| **Tool calls** | %d total |\n", total)
	fmt.Fprintf(&sb, "| **Updated** | %s |\n\n", time.Now().Format("15:04:05"))

	if len(recent) > 0 {
		sb.WriteString("**Recent activity:**\n")
		for _, e := range recent {
			icon := "✓"
			if !e.Success {
				icon = "✗"
			}
			fmt.Fprintf(&sb, "- `%s` %s\n", e.Tool, icon)
		}
	}

	return sb.String()
}

// progressTool wraps any tool and records each execution in the progressTracker.
type progressTool struct {
	inner interface {
		Name() string
		Description() string
		ParametersSchema() map[string]any
		Execute(context.Context, map[string]any) (any, error)
	}
	tracker *progressTracker
}

func (t *progressTool) Name() string                     { return t.inner.Name() }
func (t *progressTool) Description() string              { return t.inner.Description() }
func (t *progressTool) ParametersSchema() map[string]any { return t.inner.ParametersSchema() }

func (t *progressTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	result, err := t.inner.Execute(ctx, args)
	t.tracker.record(t.inner.Name(), err == nil)
	return result, err
}

// registerTool wraps t in a contextInjectingTool (injects workspace + sessionID)
// and, when tracker is non-nil, an outer progressTool (records the call).
// It registers the result in registry and marks the tool name as allowed.
// Logs a warning and skips the tool if registration fails.
func registerTool(
	registry *goframeagent.Registry,
	allowed map[string]bool,
	t mcp.Tool,
	ws *agentWorkspace,
	sessionID string,
	tracker *progressTracker,
	logger *slog.Logger,
) {
	ci := &contextInjectingTool{inner: t, projectRoot: ws.dir, sessionID: sessionID}

	var target interface {
		Name() string
		Description() string
		ParametersSchema() map[string]any
		Execute(context.Context, map[string]any) (any, error)
	}

	if tracker != nil {
		target = &progressTool{inner: ci, tracker: tracker}
	} else {
		target = ci
	}

	if err := registry.Register(target); err != nil {
		logger.Warn("failed to register tool", "tool", t.Name(), "error", err)
		return
	}
	allowed[t.Name()] = true
}
