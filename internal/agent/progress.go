package agent

// progress.go — real-time progress tracking for agent sessions.
//
// progressTracker intercepts every tool call via the progressTool wrapper and:
//   1. Writes a timestamped line to the session log file immediately.
//   2. Maintains an in-memory list of recent calls.
//   3. Posts a GitHub issue comment every 30 seconds (when new calls occurred).
//
// This gives live visibility into long GLM-5.1 / MiniMax M2.7 runs without
// requiring any changes to goframe's AgentLoop.

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
// and posts periodic GitHub progress comments.
type progressTracker struct {
	session *Session
	logW    io.Writer // session log file

	// postComment posts a body string to the GitHub issue.
	// It is a no-op closure when the GitHub client is unavailable.
	postComment func(ctx context.Context, body string)

	mu         sync.Mutex
	entries    []progressEntry
	lastPosted int    // number of entries at the time of the last comment
	phase      string // "planning" | "implementing" | "publishing"

	done chan struct{}
	wg   sync.WaitGroup
}

// newProgressTracker creates a tracker but does not start the background goroutine.
func newProgressTracker(session *Session, logW io.Writer, postComment func(ctx context.Context, body string)) *progressTracker {
	return &progressTracker{
		session:     session,
		logW:        logW,
		postComment: postComment,
		phase:       "implementing",
		done:        make(chan struct{}),
	}
}

// start launches the background goroutine that posts periodic GitHub comments.
// ctx is used for the comment HTTP calls; the goroutine itself runs until stop().
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
func (pt *progressTracker) record(tool string, success bool) {
	pt.mu.Lock()
	phase := pt.phase
	entry := progressEntry{
		Phase:   phase,
		Tool:    tool,
		Success: success,
		At:      time.Now(),
	}
	pt.entries = append(pt.entries, entry)
	total := len(pt.entries)
	pt.mu.Unlock()

	// Write to log file immediately for real-time tail visibility.
	status := "ok"
	if !success {
		status = "error"
	}
	fmt.Fprintf(pt.logW, "[%s] [%s] TOOL %-25s %s\n",
		entry.At.Format("15:04:05"), strings.ToUpper(phase), tool, status)

	_ = total // used below
}

// maybePostComment posts a GitHub comment if new tool calls have occurred since
// the last post. It is safe to call from any goroutine.
func (pt *progressTracker) maybePostComment(ctx context.Context) {
	pt.mu.Lock()
	total := len(pt.entries)
	if total == pt.lastPosted {
		pt.mu.Unlock()
		return // nothing new
	}

	phase := pt.phase
	// Grab the last N entries for display.
	start := max(0, total-progressRecentWindow)
	recent := make([]progressEntry, total-start)
	copy(recent, pt.entries[start:])
	pt.lastPosted = total
	pt.mu.Unlock()

	body := pt.buildCommentBody(phase, total, recent)
	// Post in a goroutine so a slow GitHub API call does not block the next tick.
	go pt.postComment(ctx, body)
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
// It sits between the goframe registry and contextInjectingTool in the call chain.
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
